package encoder

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"
)

// SizeEstimate predicts the size of the GIF Encode would write.
type SizeEstimate struct {
	Bytes     int64 // best guess
	Low, High int64 // plausible range; equal to Bytes when Exact
	Width     int
	Height    int
	Duration  time.Duration // how long the GIF plays, after trimming and speeding up
	// Exact is true when the whole clip was encoded, so Bytes is the size
	// Encode will produce.
	Exact bool
}

// Sampling: clips up to wholeClipMax are encoded whole; longer ones are
// estimated from sampleCount windows of sampleLength each.
const (
	wholeClipMax = 3 * time.Second
	sampleLength = time.Second
	sampleCount  = 3
	// rangeMargin widens the range beyond the slowest and fastest windows to
	// allow for sampling error (each window gets its own palette, and is cut
	// mid-motion). On a test pattern the estimate was 8% under.
	rangeMargin = 0.15
)

// Estimate predicts the size of the GIF that Encode would make from the first
// full of the video at in (full is its length, from ProbeDuration). It encodes
// a few short windows of the clip with the real pipeline and scales up, since
// GIF size depends mostly on how much of each frame changes, which no formula
// can know.
func Estimate(ctx context.Context, tools Tools, in string, full time.Duration, opts Options) (SizeEstimate, error) {
	if err := opts.Validate(); err != nil {
		return SizeEstimate{}, err
	}
	kept := KeepDuration(full, opts.TrimEnd)
	windows := sampleWindows(kept, opts.speed())

	tmp, err := os.MkdirTemp("", "prgif-estimate-")
	if err != nil {
		return SizeEstimate{}, err
	}
	defer func() { _ = os.RemoveAll(tmp) }() // best-effort temp cleanup
	bg, err := makeBackdrop(ctx, tools.FFmpeg, in, tmp, opts)
	if err != nil {
		return SizeEstimate{}, fmt.Errorf("estimating size: %w", err)
	}

	// Encode the windows concurrently. Each goroutine writes only its own
	// slot, so no lock is needed. The first failure cancels the rest.
	sampleCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	samples := make([]sample, len(windows))
	errs := make([]error, len(windows))
	var wg sync.WaitGroup
	for i, w := range windows {
		wg.Go(func() {
			samples[i], errs[i] = encodeSample(sampleCtx, tools, in, filepath.Join(tmp, strconv.Itoa(i)), w, opts, bg)
			if errs[i] != nil {
				cancel()
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return SizeEstimate{}, err
	}
	// Report the failure that caused the cancellation, not its side effects.
	for _, err := range errs {
		if err != nil && !errors.Is(err, context.Canceled) {
			return SizeEstimate{}, err
		}
	}

	est := scaleEstimate(samples, windows, kept)
	est.Width, est.Height = samples[0].width, samples[0].height
	est.Duration = opts.OutputDuration(kept)
	return est, nil
}

// sampleWindows chooses which parts of a clip of length kept to encode: the
// whole clip if it's short, otherwise windows at its start, middle and end,
// since recordings are often quiet at the ends and busy in the middle.
// Lengths are measured in GIF playback time, so a sped-up clip gets longer
// windows of the recording rather than windows with fewer frames.
func sampleWindows(kept time.Duration, speed float64) []Span {
	if kept <= time.Duration(float64(wholeClipMax)*speed) {
		return []Span{{Length: kept}}
	}
	length := time.Duration(float64(sampleLength) * speed)
	windows := make([]Span, sampleCount)
	last := kept - length
	for i := range windows {
		windows[i] = Span{Start: last * time.Duration(i) / (sampleCount - 1), Length: length}
	}
	return windows
}

// sample is the result of encoding one window.
type sample struct {
	size int64 // whole file, in bytes
	// changed is the bytes of every frame after the first. They grow with
	// the clip's length; the header, palette and first (full) frame don't.
	changed       int64
	width, height int
}

// scaleEstimate turns samples of windows into an estimate for a clip of
// length kept. The fixed part of the file comes from the first window, which
// starts where the GIF does; the rest grows at the windows' rate of changed
// bytes per second. The range uses the slowest and fastest windows, widened
// by rangeMargin.
func scaleEstimate(samples []sample, windows []Span, kept time.Duration) SizeEstimate {
	fixed := samples[0].size - samples[0].changed
	est := SizeEstimate{Duration: kept}
	if len(windows) == 1 && windows[0].Start == 0 && windows[0].Length >= kept {
		est.Bytes, est.Low, est.High, est.Exact = samples[0].size, samples[0].size, samples[0].size, true
		return est
	}

	var sum, lo, hi float64
	for i, s := range samples {
		rate := float64(s.changed) / windows[i].Length.Seconds()
		sum += rate
		if i == 0 || rate < lo {
			lo = rate
		}
		if i == 0 || rate > hi {
			hi = rate
		}
	}
	scale := func(rate float64) int64 { return fixed + int64(rate*kept.Seconds()) }
	est.Bytes = scale(sum / float64(len(samples)))
	est.Low = int64(math.Round(float64(scale(lo)) * (1 - rangeMargin)))
	est.High = int64(math.Round(float64(scale(hi)) * (1 + rangeMargin)))
	return est
}

// encodeSample runs the full pipeline on window w of in, in its own folder
// dir, and measures the result. bg is the background, or nil for none.
func encodeSample(ctx context.Context, tools Tools, in, dir string, w Span, opts Options, bg *backdrop) (sample, error) {
	if err := os.Mkdir(dir, 0o700); err != nil {
		return sample{}, err
	}
	palette := filepath.Join(dir, "palette.png")
	raw := filepath.Join(dir, "raw.gif")
	out := filepath.Join(dir, "out.gif")

	if _, err := run(ctx, tools.FFmpeg, PaletteArgs(in, palette, w, opts, bg), nil); err != nil {
		return sample{}, fmt.Errorf("estimating size: %w", err)
	}
	if _, err := run(ctx, tools.FFmpeg, FramesArgs(in, palette, raw, w, opts, bg), nil); err != nil {
		return sample{}, fmt.Errorf("estimating size: %w", err)
	}
	if _, err := run(ctx, tools.Gifsicle, GifsicleArgs(raw, out, opts), nil); err != nil {
		return sample{}, fmt.Errorf("estimating size: %w", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		return sample{}, err
	}
	sizeInfo, err := exec.CommandContext(ctx, tools.Gifsicle, "--size-info", out).Output()
	if err != nil {
		return sample{}, fmt.Errorf("estimating size: reading sample: %w", err)
	}
	s, err := parseSizeInfo(string(sizeInfo))
	if err != nil {
		return sample{}, err
	}
	s.size = info.Size()
	return s, nil
}

var compressedRE = regexp.MustCompile(`(?m)^\s+compressed size (\d+)$`)

// parseSizeInfo reads dimensions and the compressed size of every frame
// after the first from `gifsicle --size-info` output.
func parseSizeInfo(out string) (sample, error) {
	screen := infoScreenRE.FindStringSubmatch(out)
	frames := compressedRE.FindAllStringSubmatch(out, -1)
	if screen == nil || len(frames) == 0 {
		return sample{}, errors.New("unexpected gifsicle --size-info output")
	}
	var s sample
	s.width, _ = strconv.Atoi(screen[1])
	s.height, _ = strconv.Atoi(screen[2])
	for _, f := range frames[1:] {
		n, _ := strconv.ParseInt(f[1], 10, 64)
		s.changed += n
	}
	return s, nil
}
