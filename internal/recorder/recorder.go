// Package recorder captures a screen region to a video file with ffmpeg's
// avfoundation input (macOS).
package recorder

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/monsteronfire/prgif/internal/selector"
)

// CaptureFPS is the recording frame rate. The encoder reduces it later, so
// the GIF frame rate can change without re-recording.
const CaptureFPS = 30

// How long Stop waits for ffmpeg to finish after asking it to quit, and again
// after interrupting it, before killing it. Variables so tests can shorten them.
var (
	quitTimeout      = 5 * time.Second
	interruptTimeout = 5 * time.Second
)

// Recorder is a running ffmpeg capture.
type Recorder struct {
	Path    string    // output video
	Started time.Time // when ffmpeg was launched

	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stderr bytes.Buffer // read only after done is closed

	done    chan struct{} // closed when ffmpeg exits
	waitErr error         // set before done is closed

	stopOnce sync.Once
	stopErr  error
}

// Start begins recording region r to out, which should end in .mov.
func Start(ffmpeg string, r selector.Region, out string) (*Recorder, error) {
	return start(ffmpeg, Args(r, out), out)
}

// Args builds the ffmpeg arguments for recording region r to out.
//
// The capture is cropped to the region and stored losslessly (x264 qp 0,
// 4:4:4 chroma, so thin text and coloured UI stay crisp), with the ultrafast
// preset to keep up in real time. A keyframe every second keeps seeking cheap
// when the size estimate encodes windows from the middle of the recording.
func Args(r selector.Region, out string) []string {
	return []string{
		"-hide_banner", "-nostats", "-loglevel", "warning", "-y",
		"-f", "avfoundation",
		"-capture_cursor", "1",
		"-framerate", fmt.Sprint(CaptureFPS),
		"-i", fmt.Sprintf("Capture screen %d:none", r.ScreenIndex),
		"-vf", fmt.Sprintf("crop=%d:%d:%d:%d", r.W, r.H, r.X, r.Y),
		"-c:v", "libx264", "-preset", "ultrafast", "-qp", "0", "-pix_fmt", "yuv444p",
		"-g", fmt.Sprint(CaptureFPS),
		out,
	}
}

func start(ffmpeg string, args []string, out string) (*Recorder, error) {
	cmd := exec.Command(ffmpeg, args...)
	rec := &Recorder{Path: out, cmd: cmd, done: make(chan struct{})}
	cmd.Stderr = &rec.stderr

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	rec.stdin = stdin

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("starting ffmpeg: %w", err)
	}
	rec.Started = time.Now()
	go func() {
		rec.waitErr = cmd.Wait()
		close(rec.done)
	}()
	return rec, nil
}

// Done is closed when ffmpeg exits, whether stopped or failed.
func (r *Recorder) Done() <-chan struct{} { return r.done }

// Err describes why ffmpeg exited on its own, e.g. a missing Screen Recording
// permission or a crop outside the screen. Call it only after Done is closed.
func (r *Recorder) Err() error {
	<-r.done
	return r.failure(r.waitErr)
}

// Stop ends the recording cleanly and waits for ffmpeg to finalise the file.
// It sends ffmpeg's interactive "q" command first, as a kill would leave the
// .mov without its index; then falls back to SIGINT and finally SIGKILL.
// It returns an error if no usable recording was written. Safe to call more
// than once.
func (r *Recorder) Stop() error {
	r.stopOnce.Do(func() { r.stopErr = r.stop() })
	return r.stopErr
}

func (r *Recorder) stop() error {
	select {
	case <-r.done:
		// ffmpeg already exited on its own; that's a failure.
		return r.failure(r.waitErr)
	default:
	}

	_, _ = io.WriteString(r.stdin, "q")
	_ = r.stdin.Close()

	interrupted := false
	select {
	case <-r.done:
	case <-time.After(quitTimeout):
		interrupted = true
		_ = r.cmd.Process.Signal(os.Interrupt)
		select {
		case <-r.done:
		case <-time.After(interruptTimeout):
			_ = r.cmd.Process.Kill()
			<-r.done
			return errors.New("ffmpeg did not stop; the recording was discarded")
		}
	}

	// ffmpeg exits non-zero when interrupted even though the file is fine.
	if r.waitErr != nil && !interrupted {
		return r.failure(r.waitErr)
	}
	if info, err := os.Stat(r.Path); err != nil || info.Size() == 0 {
		return r.failure(errors.New("no video was written"))
	}
	return nil
}

// Discard kills ffmpeg without finalising and deletes the output.
func (r *Recorder) Discard() {
	r.stopOnce.Do(func() {
		_ = r.cmd.Process.Kill()
		<-r.done
		_ = os.Remove(r.Path)
		r.stopErr = errors.New("recording discarded")
	})
}

// failure wraps err with the tail of ffmpeg's stderr.
func (r *Recorder) failure(err error) error {
	if err == nil {
		err = errors.New("ffmpeg exited unexpectedly")
	}
	msg := strings.TrimSpace(r.stderr.String())
	if msg == "" {
		return fmt.Errorf("recording failed: %w", err)
	}
	lines := strings.Split(msg, "\n")
	if len(lines) > 8 {
		lines = lines[len(lines)-8:]
	}
	return fmt.Errorf("recording failed: %w\n%s", err, strings.Join(lines, "\n"))
}

// TempPath returns a path for a new recording in a fresh temporary directory.
// The caller removes the directory when done with the recording.
func TempPath() (path, dir string, err error) {
	dir, err = os.MkdirTemp("", "prgif-rec-")
	if err != nil {
		return "", "", err
	}
	return filepath.Join(dir, "recording.mov"), dir, nil
}
