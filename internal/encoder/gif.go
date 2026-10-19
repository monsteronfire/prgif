// Package encoder turns a screen recording into an optimised GIF.
//
// The pipeline is two ffmpeg passes (build a palette tuned to the clip, then
// map frames onto it) followed by a lossy gifsicle pass to shrink the file.
package encoder

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Options control the size/quality trade-off of the output GIF.
type Options struct {
	FPS      int // output frame rate
	MaxWidth int // output width cap in pixels; smaller inputs are never upscaled
	Lossy    int // gifsicle --lossy level, 0 (off) to 200; higher is smaller and noisier
	// TrimEnd cuts this much from the end of the clip, e.g. the moment of
	// switching back to the terminal to stop recording.
	TrimEnd time.Duration
	// Speed multiplies playback speed, from 1 to MaxSpeed; 2 makes the GIF
	// half as long. 0 means 1.
	Speed float64
	// Background frames the recording on a gradient. The zero value is none.
	Background Background
}

// DefaultOptions suit typical UI recordings in a PR description.
var DefaultOptions = Options{FPS: 12, MaxWidth: 800, Lossy: 80, TrimEnd: 500 * time.Millisecond, Speed: 1}

// MaxTrimEnd is the largest allowed TrimEnd.
const MaxTrimEnd = 10 * time.Second

// MaxSpeed is the largest allowed Speed.
const MaxSpeed = 4

// speed returns Speed, with 0 meaning normal speed.
func (o Options) speed() float64 {
	if o.Speed == 0 {
		return 1
	}
	return o.Speed
}

// OutputDuration is how long the GIF plays for a kept clip of length kept,
// after speeding it up.
func (o Options) OutputDuration(kept time.Duration) time.Duration {
	return time.Duration(float64(kept) / o.speed())
}

// minKeep is the shortest clip trimming will leave. A clip shorter than
// TrimEnd plus this is trimmed less, rather than emptied.
const minKeep = 500 * time.Millisecond

// Validate reports whether the options are usable.
func (o Options) Validate() error {
	switch {
	case o.FPS < 1 || o.FPS > 50:
		return fmt.Errorf("fps must be between 1 and 50, got %d", o.FPS)
	case o.MaxWidth < 16:
		return fmt.Errorf("max width must be at least 16, got %d", o.MaxWidth)
	case o.Lossy < 0 || o.Lossy > 200:
		return fmt.Errorf("lossy must be between 0 and 200, got %d", o.Lossy)
	case o.TrimEnd < 0 || o.TrimEnd > MaxTrimEnd:
		return fmt.Errorf("trim must be between 0s and %s, got %s", MaxTrimEnd, o.TrimEnd)
	case o.Speed != 0 && (o.Speed < 1 || o.Speed > MaxSpeed):
		return fmt.Errorf("speed must be between 1 and %d, got %g", MaxSpeed, o.Speed)
	case o.Background.Padding < 0 || o.Background.Padding > MaxPadding:
		return fmt.Errorf("background padding must be between 0 and %d%%, got %d%%", MaxPadding, o.Background.Padding)
	}
	return nil
}

// Tools holds the paths of the external programs to run.
type Tools struct {
	FFmpeg   string
	Gifsicle string
}

// Stage identifies which part of the pipeline is running.
type Stage int

const (
	StagePalette  Stage = iota // ffmpeg pass 1: palettegen
	StageFrames                // ffmpeg pass 2: paletteuse
	StageOptimise              // gifsicle
)

func (s Stage) String() string {
	switch s {
	case StagePalette:
		return "Building palette"
	case StageFrames:
		return "Encoding frames"
	case StageOptimise:
		return "Optimising"
	}
	return "Unknown"
}

// Progress is reported while encoding. Fraction is the overall completion
// from 0 to 1 across all stages.
type Progress struct {
	Stage    Stage
	Fraction float64
}

// Result describes the written GIF.
type Result struct {
	Path     string
	Size     int64 // bytes
	Width    int
	Height   int
	Frames   int
	Duration time.Duration // how long the GIF plays, after trimming and speeding up
	Trimmed  time.Duration // how much was cut from the end of the recording
}

// Share of overall progress given to each stage. Pass 2 dominates the runtime.
const (
	paletteShare  = 0.15
	framesShare   = 0.70
	optimiseShare = 0.15
)

// Encode converts the video at in to an optimised GIF at out. progress may be
// nil; it is called from the encoding goroutine.
func Encode(ctx context.Context, tools Tools, in, out string, opts Options, progress func(Progress)) (Result, error) {
	if err := opts.Validate(); err != nil {
		return Result{}, err
	}
	if progress == nil {
		progress = func(Progress) {}
	}

	tmp, err := os.MkdirTemp("", "prgif-encode-")
	if err != nil {
		return Result{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // best-effort temp cleanup
	palette := filepath.Join(tmp, "palette.png")
	raw := filepath.Join(tmp, "raw.gif")

	// The clip length is needed up front, both to trim it and to turn
	// ffmpeg's timestamps into progress.
	progress(Progress{Stage: StagePalette})
	full, err := ProbeDuration(ctx, tools.FFmpeg, in)
	if err != nil {
		return Result{}, err
	}
	duration := KeepDuration(full, opts.TrimEnd)
	span := Span{Length: duration}
	// ffmpeg's progress timestamps are in output time, after speeding up.
	playback := opts.OutputDuration(duration)

	bg, err := makeBackdrop(ctx, tools.FFmpeg, in, tmp, opts)
	if err != nil {
		return Result{}, err
	}

	// Pass 1: palette.
	if _, err := run(ctx, tools.FFmpeg, PaletteArgs(in, palette, span, opts, bg), nil); err != nil {
		return Result{}, fmt.Errorf("building palette: %w", err)
	}

	// Pass 2: frames, reporting progress via -progress pipe:1.
	progress(Progress{Stage: StageFrames, Fraction: paletteShare})
	onStdout := func(r io.Reader) {
		// Progress is best-effort: a read error here only means fewer updates,
		// and a real failure surfaces from ffmpeg's exit status instead.
		_ = ParseProgress(r, playback, func(f float64) {
			progress(Progress{Stage: StageFrames, Fraction: paletteShare + f*framesShare})
		})
	}
	if _, err := run(ctx, tools.FFmpeg, FramesArgs(in, palette, raw, span, opts, bg), onStdout); err != nil {
		return Result{}, fmt.Errorf("encoding frames: %w", err)
	}

	// Pass 3: lossy optimisation, written straight to the destination.
	// gifsicle reports nothing until it finishes, so estimate its progress.
	progress(Progress{Stage: StageOptimise, Fraction: paletteShare + framesShare})
	stopEstimate := estimateOptimise(raw, func(f float64) {
		progress(Progress{Stage: StageOptimise, Fraction: paletteShare + framesShare + f*optimiseShare})
	})
	_, err = run(ctx, tools.Gifsicle, GifsicleArgs(raw, out, opts), nil)
	stopEstimate()
	if err != nil {
		_ = os.Remove(out) // don't leave a truncated GIF behind
		return Result{}, fmt.Errorf("optimising: %w", err)
	}
	progress(Progress{Stage: StageOptimise, Fraction: 1})

	res, err := inspect(ctx, tools, out)
	if err != nil {
		return Result{}, err
	}
	res.Duration, res.Trimmed = playback, full-duration
	return res, nil
}

// KeepDuration is how much of a clip of length full remains after trimming
// trim from its end. It never leaves less than minKeep (or the whole clip, if
// shorter), so a short recording isn't trimmed to nothing.
func KeepDuration(full, trim time.Duration) time.Duration {
	if trim <= 0 {
		return full
	}
	return max(full-trim, min(full, minKeep))
}

// ProbeDuration reads the length of the video at in from ffmpeg's banner.
// ffmpeg exits non-zero without an output file; only the banner matters.
func ProbeDuration(ctx context.Context, ffmpeg, in string) (time.Duration, error) {
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-i", in).CombinedOutput()
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	d, ok := ParseDuration(string(out))
	if !ok {
		return 0, fmt.Errorf("reading %s: no duration found\n%s", filepath.Base(in), tail(string(out), 5))
	}
	return d, nil
}

// optimiseSecondsPerMB is a rough measure of gifsicle -O2 --lossy throughput
// on Apple Silicon: seconds per MB of the unoptimised GIF.
var optimiseSecondsPerMB = 0.6

// estimateOptimise reports estimated gifsicle progress (0 to 1) from the size
// of its input until the returned stop function is called. The estimate eases
// towards 0.95, so it slows down rather than stalling at 100% when gifsicle
// takes longer than expected.
func estimateOptimise(raw string, report func(float64)) (stop func()) {
	expected := time.Second
	if info, err := os.Stat(raw); err == nil {
		expected = max(time.Duration(float64(info.Size())/1e6*optimiseSecondsPerMB*float64(time.Second)), 200*time.Millisecond)
	}

	done := make(chan struct{})
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		start := time.Now()
		tick := time.NewTicker(100 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-done:
				return
			case <-tick.C:
				report(0.95 * (1 - math.Exp(-2*float64(time.Since(start))/float64(expected))))
			}
		}
	}()
	return func() {
		close(done)
		<-finished // no report after stop returns
	}
}

// scaleFilter speeds up the clip, picks frames at the output fps, and caps
// the width without upscaling, keeping the aspect ratio. setpts comes first
// so fps samples the sped-up timeline.
//
// With a background, the recording is scaled to fit inside it, padded out to
// the GIF's size, and the background (input 1) laid over it, showing the
// recording through its hole. format=rgb24 then drops the alpha channel the
// overlay leaves behind: ffmpeg 8.1's paletteuse rejects it.
func scaleFilter(opts Options, bg *backdrop) string {
	scale := fmt.Sprintf("scale='min(%d,iw)':-1:flags=lanczos", opts.MaxWidth)
	if bg != nil {
		l := bg.layout
		scale = fmt.Sprintf("scale=%d:%d:flags=lanczos,pad=%d:%d:%d:%d[v];[v][1:v]overlay=format=rgb,format=rgb24",
			l.ContentW, l.ContentH, l.Width, l.Height, l.Pad, l.Pad)
	}
	f := fmt.Sprintf("fps=%d,", opts.FPS) + scale
	if s := opts.speed(); s != 1 {
		f = "setpts=PTS/" + strconv.FormatFloat(s, 'f', -1, 64) + "," + f
	}
	return f
}

// backdropArgs adds the background image, if any, as input 1.
func backdropArgs(bg *backdrop) []string {
	if bg == nil {
		return nil
	}
	return []string{"-i", bg.path}
}

// Span is the part of the input to read: Length from Start. A zero Length
// means to the end.
type Span struct {
	Start, Length time.Duration
}

// inputArgs reads span of in. -ss and -t go before -i so they limit the
// input rather than the output.
func inputArgs(in string, span Span) []string {
	var args []string
	if span.Start > 0 {
		args = append(args, "-ss", seconds(span.Start))
	}
	if span.Length > 0 {
		args = append(args, "-t", seconds(span.Length))
	}
	return append(args, "-i", in)
}

// seconds formats d for ffmpeg, e.g. "2.500".
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}

// PaletteArgs builds the ffmpeg arguments for pass 1, reading span of in.
// stats_mode=diff weights the palette towards pixels that change, which suits
// mostly-static UI. bg is the background, or nil for none.
func PaletteArgs(in, palette string, span Span, opts Options, bg *backdrop) []string {
	return slices.Concat([]string{"-hide_banner", "-nostdin", "-y"}, inputArgs(in, span), backdropArgs(bg), []string{
		"-lavfi", scaleFilter(opts, bg) + ",palettegen=" + paletteGen(bg),
		palette,
	})
}

// paletteGen configures palettegen. A background never changes, so
// stats_mode=diff only counts it in the first frame, and on a busy clip the
// moving pixels crowd it out of the palette and the gradient bands. Counting
// every pixel (full) keeps it smooth.
func paletteGen(bg *backdrop) string {
	if bg != nil {
		return "stats_mode=full:max_colors=256"
	}
	return "stats_mode=diff:max_colors=256"
}

// FramesArgs builds the ffmpeg arguments for pass 2, reading span of in.
// diff_mode=rectangle only re-dithers the changed area of each frame, which
// keeps static regions identical between frames so gifsicle can drop them.
// bg is the background, or nil for none; it comes before the palette.
func FramesArgs(in, palette, out string, span Span, opts Options, bg *backdrop) []string {
	paletteInput := "[1:v]"
	if bg != nil {
		paletteInput = "[2:v]"
	}
	return slices.Concat([]string{
		"-hide_banner", "-nostdin", "-y", "-nostats",
		"-progress", "pipe:1",
	}, inputArgs(in, span), backdropArgs(bg), []string{
		"-i", palette,
		"-lavfi", scaleFilter(opts, bg) + "[x];[x]" + paletteInput + "paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle",
		"-loop", "0",
		out,
	})
}

// GifsicleArgs builds the gifsicle arguments for the final pass. -O2 rather
// than -O3: with --lossy, -O3 takes about three times as long for output
// within 0.1% of the size.
func GifsicleArgs(in, out string, opts Options) []string {
	args := []string{"-O2"}
	if opts.Lossy > 0 {
		args = append(args, "--lossy="+strconv.Itoa(opts.Lossy))
	}
	return append(args, "--colors", "256", in, "-o", out)
}

var durationRE = regexp.MustCompile(`Duration: (\d+):(\d{2}):(\d{2}(?:\.\d+)?)`)

// ParseDuration extracts the input duration from ffmpeg's stderr banner.
func ParseDuration(stderr string) (time.Duration, bool) {
	m := durationRE.FindStringSubmatch(stderr)
	if m == nil {
		return 0, false
	}
	h, _ := strconv.Atoi(m[1])
	mins, _ := strconv.Atoi(m[2])
	secs, _ := strconv.ParseFloat(m[3], 64)
	d := time.Duration(h)*time.Hour + time.Duration(mins)*time.Minute + time.Duration(secs*float64(time.Second))
	return d, d > 0
}

// ParseProgress reads ffmpeg -progress key=value output and calls report with
// the fraction of duration processed, clamped to [0, 1]. Without a duration
// only the final "progress=end" is reported. It returns any read error.
func ParseProgress(r io.Reader, duration time.Duration, report func(float64)) error {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "out_time_us":
			us, err := strconv.ParseInt(v, 10, 64)
			if err != nil || duration <= 0 {
				continue
			}
			report(min(max(float64(us)*float64(time.Microsecond)/float64(duration), 0), 1))
		case "progress":
			if v == "end" {
				report(1)
			}
		}
	}
	return sc.Err()
}

// run executes name with args. onStdout, if non-nil, consumes stdout while the
// command runs. It returns stderr, which is folded into the error on failure.
func run(ctx context.Context, name string, args []string, onStdout func(io.Reader)) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	var stdout io.ReadCloser
	if onStdout != nil {
		var err error
		if stdout, err = cmd.StdoutPipe(); err != nil {
			return "", err
		}
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	if stdout != nil {
		onStdout(stdout)
		_, _ = io.Copy(io.Discard, stdout) // drain anything the parser left
	}
	if err := cmd.Wait(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return stderr.String(), ctxErr
		}
		return stderr.String(), fmt.Errorf("%s: %w\n%s", filepath.Base(name), err, tail(stderr.String(), 10))
	}
	return stderr.String(), nil
}

// tail returns the last n lines of s.
func tail(s string, n int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// inspect reads size, dimensions and frame count from the finished GIF.
// gifsicle --info avoids decoding every frame into memory.
func inspect(ctx context.Context, tools Tools, path string) (Result, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Result{}, err
	}
	out, err := exec.CommandContext(ctx, tools.Gifsicle, "--info", path).Output()
	if err != nil {
		return Result{}, fmt.Errorf("reading output: %w", err)
	}
	res, err := ParseInfo(string(out))
	if err != nil {
		return Result{}, err
	}
	res.Path, res.Size = path, info.Size()
	return res, nil
}

var (
	infoFramesRE = regexp.MustCompile(`(?m)^\* .* (\d+) images?$`)
	infoScreenRE = regexp.MustCompile(`logical screen (\d+)x(\d+)`)
)

// ParseInfo extracts frame count and dimensions from `gifsicle --info` output.
func ParseInfo(out string) (Result, error) {
	frames := infoFramesRE.FindStringSubmatch(out)
	screen := infoScreenRE.FindStringSubmatch(out)
	if frames == nil || screen == nil {
		return Result{}, errors.New("unexpected gifsicle --info output")
	}
	var r Result
	r.Frames, _ = strconv.Atoi(frames[1])
	r.Width, _ = strconv.Atoi(screen[1])
	r.Height, _ = strconv.Atoi(screen[2])
	if r.Frames == 0 {
		return Result{}, errors.New("output GIF has no frames")
	}
	return r, nil
}
