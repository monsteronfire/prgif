package encoder

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestSampleWindows(t *testing.T) {
	// Short clips are encoded whole.
	for _, kept := range []time.Duration{500 * time.Millisecond, 2 * time.Second, wholeClipMax} {
		if got, want := sampleWindows(kept, 1), []Span{{Length: kept}}; !slices.Equal(got, want) {
			t.Errorf("sampleWindows(%v, 1) = %v, want %v", kept, got, want)
		}
	}

	// Longer ones get 1s windows at the start, middle and end.
	got := sampleWindows(10 * time.Second, 1)
	want := []Span{
		{Start: 0, Length: time.Second},
		{Start: 4500 * time.Millisecond, Length: time.Second},
		{Start: 9 * time.Second, Length: time.Second},
	}
	if !slices.Equal(got, want) {
		t.Errorf("sampleWindows(10s, 1) = %v, want %v", got, want)
	}
	for _, kept := range []time.Duration{wholeClipMax + time.Millisecond, 7 * time.Second, time.Minute} {
		for _, w := range sampleWindows(kept, 1) {
			if w.Start < 0 || w.Start+w.Length > kept {
				t.Errorf("sampleWindows(%v, 1): window %v outside the clip", kept, w)
			}
		}
	}
}

func TestSampleWindowsSpedUp(t *testing.T) {
	// At 2×, windows cover 2s of the recording: 1s of GIF.
	if got, want := sampleWindows(6*time.Second, 2), []Span{{Length: 6 * time.Second}}; !slices.Equal(got, want) {
		t.Errorf("sampleWindows(6s, 2) = %v, want the whole clip", got)
	}
	got := sampleWindows(20*time.Second, 2)
	want := []Span{
		{Start: 0, Length: 2 * time.Second},
		{Start: 9 * time.Second, Length: 2 * time.Second},
		{Start: 18 * time.Second, Length: 2 * time.Second},
	}
	if !slices.Equal(got, want) {
		t.Errorf("sampleWindows(20s, 2) = %v, want %v", got, want)
	}
}

func TestScaleEstimateWholeClipIsExact(t *testing.T) {
	windows := []Span{{Length: 2 * time.Second}}
	est := scaleEstimate([]sample{{size: 50_000, changed: 30_000}}, windows, 2*time.Second)

	if !est.Exact || est.Bytes != 50_000 || est.Low != 50_000 || est.High != 50_000 {
		t.Errorf("estimate = %+v, want exact 50000", est)
	}
}

func TestScaleEstimate(t *testing.T) {
	windows := sampleWindows(10 * time.Second, 1)
	samples := []sample{
		{size: 25_000, changed: 5_000},  // fixed part 20 KB; quiet start: 5 KB/s
		{size: 41_000, changed: 20_000}, // busy middle: 20 KB/s
		{size: 22_000, changed: 2_000},  // quiet end: 2 KB/s
	}

	est := scaleEstimate(samples, windows, 10*time.Second)

	// 20 KB fixed + mean(5, 20, 2) = 9 KB/s × 10s. The range is 2 KB/s and
	// 20 KB/s, widened by 15%.
	if est.Bytes != 110_000 || est.Low != 34_000 || est.High != 253_000 {
		t.Errorf("estimate = %d (%d–%d), want 110000 (34000–253000)", est.Bytes, est.Low, est.High)
	}
	if est.Exact || est.Duration != 10*time.Second {
		t.Errorf("estimate = %+v, want inexact, 10s", est)
	}
}

func TestScaleEstimateStaticClip(t *testing.T) {
	// Nothing changes after the first frame, so length doesn't matter.
	windows := sampleWindows(time.Minute, 1)
	samples := []sample{{size: 30_000}, {size: 30_000}, {size: 30_000}}

	if est := scaleEstimate(samples, windows, time.Minute); est.Bytes != 30_000 {
		t.Errorf("estimate = %+v, want 30000", est)
	}
}

func TestParseSizeInfo(t *testing.T) {
	out := `* out.gif 3 images
  logical screen 800x427
  global color table [256]
  background 13
  loop forever
  + image #0 800x427 transparent 13
    compressed size 23853
    disposal asis delay 0.08s
  + image #1 800x59 at 0,318 transparent 13
    compressed size 11097
    disposal asis delay 0.09s
  + image #2 800x59 at 0,318 transparent 24
    compressed size 11970
    disposal asis delay 0.08s
`
	s, err := parseSizeInfo(out)
	if err != nil {
		t.Fatal(err)
	}
	if s.width != 800 || s.height != 427 || s.changed != 11097+11970 {
		t.Errorf("parsed %+v, want 800x427 with 23067 changed bytes", s)
	}

	if _, err := parseSizeInfo("garbage"); err == nil {
		t.Error("no error for unexpected output")
	}
}

func TestEstimateFailureCleansUp(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\necho 'Invalid data found' >&2\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	_, err := Estimate(context.Background(), Tools{FFmpeg: ffmpeg, Gifsicle: "gifsicle"}, "in.mov", 10*time.Second, DefaultOptions)

	if err == nil {
		t.Fatal("Estimate succeeded with a failing ffmpeg")
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("temp files left behind: %v", entries)
	}
}

func TestEstimateCancelled(t *testing.T) {
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	// exec, so killing the script kills the sleep too, as with a real ffmpeg.
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\nexec sleep 10\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Estimate(ctx, Tools{FFmpeg: ffmpeg, Gifsicle: "gifsicle"}, "in.mov", 10*time.Second, DefaultOptions)

	if err != context.DeadlineExceeded {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("took %v to stop after cancellation", elapsed)
	}
}
