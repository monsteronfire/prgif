package encoder

import (
	"context"
	"fmt"
	"image/gif"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These tests run the real ffmpeg and gifsicle. They are skipped with -short
// or when either tool is missing.

func requireTools(t *testing.T) Tools {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	gifsicle, err := exec.LookPath("gifsicle")
	if err != nil {
		t.Skip("gifsicle not installed")
	}
	return Tools{FFmpeg: ffmpeg, Gifsicle: gifsicle}
}

// makeClip renders an ffmpeg test pattern, encoded the way the recorder will
// encode screen captures.
func makeClip(t *testing.T, tools Tools, w, h int) string {
	t.Helper()
	clip := filepath.Join(t.TempDir(), "clip.mov")
	cmd := exec.Command(tools.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc=duration=3:size=%dx%d:rate=30", w, h),
		"-c:v", "libx264", "-preset", "ultrafast", "-crf", "0", "-pix_fmt", "yuv444p",
		clip,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("making test clip: %v\n%s", err, out)
	}
	return clip
}

func TestEncodeIntegration(t *testing.T) {
	tools := requireTools(t)
	clip := makeClip(t, tools, 1200, 640)
	out := filepath.Join(t.TempDir(), "out.gif")

	var reports []Progress
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	opts := DefaultOptions
	opts.TrimEnd = 0
	res, err := Encode(ctx, tools, clip, out, opts, func(p Progress) { reports = append(reports, p) })
	if err != nil {
		t.Fatal(err)
	}

	// Width is capped and the aspect ratio kept: 640 * 800/1200 ≈ 427.
	if res.Width != 800 || res.Height != 427 {
		t.Errorf("size = %dx%d, want 800x427", res.Width, res.Height)
	}
	// 3s at 12fps. Allow ±1 for fps-filter rounding at the clip boundaries.
	if res.Frames < 35 || res.Frames > 37 {
		t.Errorf("frames = %d, want ~36", res.Frames)
	}
	assertValidGIF(t, out, res)

	if len(reports) == 0 || reports[len(reports)-1].Fraction != 1 {
		t.Errorf("progress did not finish at 1: %+v", reports)
	}
	for i := 1; i < len(reports); i++ {
		if reports[i].Fraction < reports[i-1].Fraction {
			t.Errorf("progress went backwards: %+v", reports)
			break
		}
	}
}

func TestEncodeTrimsEnd(t *testing.T) {
	tools := requireTools(t)
	clip := makeClip(t, tools, 640, 360) // 3s
	out := filepath.Join(t.TempDir(), "trimmed.gif")

	opts := DefaultOptions
	opts.TrimEnd = time.Second
	res, err := Encode(context.Background(), tools, clip, out, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Duration != 2*time.Second || res.Trimmed != time.Second {
		t.Errorf("duration %v trimmed %v, want 2s and 1s", res.Duration, res.Trimmed)
	}
	// 2s at 12fps. Allow ±1 for rounding at the clip boundaries.
	if res.Frames < 23 || res.Frames > 25 {
		t.Errorf("frames = %d, want ~24", res.Frames)
	}
}

func TestEncodeSpeedsUp(t *testing.T) {
	tools := requireTools(t)
	clip := makeSourceClip(t, tools, "testsrc", 8)
	out := filepath.Join(t.TempDir(), "fast.gif")

	opts := DefaultOptions
	opts.TrimEnd, opts.Speed = 0, 2
	var last float64
	res, err := Encode(context.Background(), tools, clip, out, opts, func(p Progress) { last = p.Fraction })
	if err != nil {
		t.Fatal(err)
	}
	// 8s at 2× plays for 4s: about 48 frames at 12 fps.
	if res.Duration != 4*time.Second {
		t.Errorf("duration = %v, want 4s", res.Duration)
	}
	if res.Frames < 47 || res.Frames > 49 {
		t.Errorf("frames = %d, want ~48", res.Frames)
	}
	if last != 1 {
		t.Errorf("progress ended at %v, want 1", last)
	}

	// The estimate follows the speed too.
	est, err := Estimate(context.Background(), tools, clip, 8*time.Second, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("estimate %d (%d–%d); actual %d", est.Bytes, est.Low, est.High, res.Size)
	if est.Duration != 4*time.Second || res.Size < est.Low || res.Size > est.High {
		t.Errorf("estimate %v %d–%d, actual %v %d", est.Duration, est.Low, est.High, res.Duration, res.Size)
	}
}

func TestEncodeDoesNotUpscale(t *testing.T) {
	tools := requireTools(t)
	clip := makeClip(t, tools, 400, 300)
	out := filepath.Join(t.TempDir(), "small.gif")

	res, err := Encode(context.Background(), tools, clip, out, DefaultOptions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Width != 400 || res.Height != 300 {
		t.Errorf("size = %dx%d, want 400x300", res.Width, res.Height)
	}
}

func TestEncodeMissingInput(t *testing.T) {
	tools := requireTools(t)
	out := filepath.Join(t.TempDir(), "out.gif")

	_, err := Encode(context.Background(), tools, "does-not-exist.mov", out, DefaultOptions, nil)
	if err == nil {
		t.Fatal("Encode succeeded on a missing input")
	}
	if _, statErr := os.Stat(out); statErr == nil {
		t.Error("output file written despite failure")
	}
}

// makeSourceClip renders seconds of an ffmpeg lavfi source at 1200x640, with
// a keyframe every second like the recorder.
func makeSourceClip(t *testing.T, tools Tools, source string, seconds int) string {
	t.Helper()
	clip := filepath.Join(t.TempDir(), "clip.mov")
	sep := "=" // "testsrc=size=…", but "color=c=gray:size=…"
	if strings.Contains(source, "=") {
		sep = ":"
	}
	cmd := exec.Command(tools.FFmpeg,
		"-hide_banner", "-loglevel", "error", "-nostdin", "-y",
		"-f", "lavfi", "-i", source+sep+"size=1200x640:rate=30", "-t", fmt.Sprint(seconds),
		"-c:v", "libx264", "-preset", "ultrafast", "-qp", "0", "-pix_fmt", "yuv444p", "-g", "30",
		clip,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("making test clip: %v\n%s", err, out)
	}
	return clip
}

func TestEstimateMatchesEncode(t *testing.T) {
	tools := requireTools(t)
	for _, tt := range []struct {
		source    string
		seconds   int
		wantExact bool
	}{
		{"color=c=gray", 8, false}, // nothing moves
		{"testsrc", 8, false},      // everything moves
		{"testsrc", 3, true},       // short enough to encode whole (after the 0.5s trim)
	} {
		t.Run(fmt.Sprintf("%s %ds", tt.source, tt.seconds), func(t *testing.T) {
			clip := makeSourceClip(t, tools, tt.source, tt.seconds)
			ctx := context.Background()
			full, err := ProbeDuration(ctx, tools.FFmpeg, clip)
			if err != nil {
				t.Fatal(err)
			}

			start := time.Now()
			est, err := Estimate(ctx, tools, clip, full, DefaultOptions)
			if err != nil {
				t.Fatal(err)
			}
			took := time.Since(start)
			res, err := Encode(ctx, tools, clip, filepath.Join(t.TempDir(), "out.gif"), DefaultOptions, nil)
			if err != nil {
				t.Fatal(err)
			}

			ratio := float64(est.Bytes) / float64(res.Size)
			t.Logf("estimate %d (%d–%d) in %v; actual %d; ratio %.2f", est.Bytes, est.Low, est.High, took, res.Size, ratio)
			if est.Exact != tt.wantExact {
				t.Errorf("Exact = %v, want %v", est.Exact, tt.wantExact)
			}
			if tt.wantExact && est.Bytes != res.Size {
				t.Errorf("exact estimate %d, actual %d", est.Bytes, res.Size)
			}
			if ratio < 0.65 || ratio > 1.35 {
				t.Errorf("estimate %d is more than 35%% off the actual %d", est.Bytes, res.Size)
			}
			if res.Size < est.Low || res.Size > est.High {
				t.Errorf("actual %d outside the estimated range %d–%d", res.Size, est.Low, est.High)
			}
			if est.Width != res.Width || est.Height != res.Height || est.Duration != res.Duration {
				t.Errorf("estimate %dx%d %v, actual %dx%d %v", est.Width, est.Height, est.Duration, res.Width, res.Height, res.Duration)
			}
		})
	}
}

// assertValidGIF decodes the header independently of gifsicle.
func assertValidGIF(t *testing.T, path string, res Result) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }() // read-only
	cfg, err := gif.DecodeConfig(f)
	if err != nil {
		t.Fatalf("not a valid GIF: %v", err)
	}
	if cfg.Width != res.Width || cfg.Height != res.Height {
		t.Errorf("header %dx%d disagrees with result %dx%d", cfg.Width, cfg.Height, res.Width, res.Height)
	}
	info, _ := f.Stat()
	if info.Size() != res.Size {
		t.Errorf("Result.Size = %d, file is %d", res.Size, info.Size())
	}
	t.Logf("%s: %dx%d, %d frames, %.1f KB", filepath.Base(path), res.Width, res.Height, res.Frames, float64(res.Size)/1024)
}
