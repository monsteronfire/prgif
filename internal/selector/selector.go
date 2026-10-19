// Package selector runs the select-region overlay helper and converts its
// output into a pixel region that ffmpeg can crop to.
package selector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"strconv"
	"strings"
)

// ErrCancelled means the user dismissed the overlay without selecting.
var ErrCancelled = errors.New("selection cancelled")

// Region is a selected screen area in pixels, relative to the top-left corner
// of the display it is on.
type Region struct {
	X, Y, W, H  int
	DisplayID   uint32  // CGDirectDisplayID
	ScreenIndex int     // N in ffmpeg avfoundation's "Capture screen N"
	Scale       float64 // pixels per point; 2 on Retina displays
	// CanFrame says the helper that picked the region can draw a border
	// around it with ShowFrame. Older helpers can't.
	CanFrame bool
}

// String renders the region as "1200×640 at 824,376".
func (r Region) String() string {
	return fmt.Sprintf("%d×%d at %d,%d", r.W, r.H, r.X, r.Y)
}

// IsZero reports whether no region has been selected.
func (r Region) IsZero() bool { return r.W == 0 || r.H == 0 }

// helperOutput is the JSON the helper prints. Values are in points.
type helperOutput struct {
	X           *float64 `json:"x"`
	Y           *float64 `json:"y"`
	W           *float64 `json:"w"`
	H           *float64 `json:"h"`
	Scale       *float64 `json:"scale"`
	DisplayID   *uint32  `json:"displayID"`
	ScreenIndex *int     `json:"screenIndex"`
	Frame       bool     `json:"frame"`
}

// Run shows the overlay and waits for the user to select an area. It returns
// ErrCancelled if they press Esc or right-click, and ctx.Err() if ctx is
// cancelled first (which also closes the overlay).
func Run(ctx context.Context, helper string) (Region, error) {
	cmd := exec.CommandContext(ctx, helper)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Region{}, ctxErr
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return Region{}, ErrCancelled
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return Region{}, fmt.Errorf("select-region: %w", err)
		}
		return Region{}, fmt.Errorf("select-region: %w: %s", err, msg)
	}
	return Parse(out)
}

// Parse converts the helper's JSON into a pixel Region. Coordinates are
// rounded to whole pixels and the size is rounded down to even numbers, which
// video encoders prefer.
func Parse(data []byte) (Region, error) {
	var o helperOutput
	if err := json.Unmarshal(data, &o); err != nil {
		return Region{}, fmt.Errorf("parsing select-region output %q: %w", strings.TrimSpace(string(data)), err)
	}
	if o.X == nil || o.Y == nil || o.W == nil || o.H == nil || o.Scale == nil || o.DisplayID == nil {
		return Region{}, fmt.Errorf("select-region output is missing fields: %s", strings.TrimSpace(string(data)))
	}
	if o.ScreenIndex == nil {
		return Region{}, errors.New("select-region is out of date (no screenIndex); rebuild it with make helper")
	}
	if *o.Scale <= 0 || *o.ScreenIndex < 0 {
		return Region{}, fmt.Errorf("select-region reported invalid scale %v or screen index %d", *o.Scale, *o.ScreenIndex)
	}

	s := *o.Scale
	r := Region{
		X:           int(math.Round(*o.X * s)),
		Y:           int(math.Round(*o.Y * s)),
		W:           evenFloor(*o.W * s),
		H:           evenFloor(*o.H * s),
		DisplayID:   *o.DisplayID,
		ScreenIndex: *o.ScreenIndex,
		Scale:       s,
		CanFrame:    o.Frame,
	}
	if r.X < 0 || r.Y < 0 {
		return Region{}, fmt.Errorf("select-region reported a negative position: %s", r)
	}
	if r.W < 2 || r.H < 2 {
		return Region{}, fmt.Errorf("selected area is too small: %s", r)
	}
	return r, nil
}

// evenFloor rounds v to the nearest pixel, then down to an even number.
func evenFloor(v float64) int {
	n := int(math.Round(v))
	return n - n%2
}

// Frame is the on-screen marking of a region drawn by ShowFrame.
type Frame struct {
	cmd   *exec.Cmd
	stdin io.Closer
}

// ShowFrame runs helper to dim the screen outside r and draw a dotted border
// just outside it, so the pointer can be kept inside the area while
// recording. Nothing is drawn inside the area, so none of it is recorded. It stays until Close, or until this process exits:
// the helper quits when its stdin closes.
func ShowFrame(helper string, r Region) (*Frame, error) {
	points := func(px int) string { return strconv.FormatFloat(float64(px)/r.Scale, 'f', -1, 64) }
	cmd := exec.Command(helper, "--frame", strconv.FormatUint(uint64(r.DisplayID), 10),
		points(r.X), points(r.Y), points(r.W), points(r.H))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("select-region --frame: %w", err)
	}
	return &Frame{cmd: cmd, stdin: stdin}, nil
}

// Close removes the marking and waits for the helper to exit.
func (f *Frame) Close() {
	_ = f.stdin.Close()
	_ = f.cmd.Process.Kill() // don't wait on the helper noticing
	_ = f.cmd.Wait()         // reap it; the exit status doesn't matter
}
