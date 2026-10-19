package recorder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/monsteronfire/prgif/internal/encoder"
	"github.com/monsteronfire/prgif/internal/selector"
)

var region = selector.Region{X: 824, Y: 376, W: 1200, H: 640, ScreenIndex: 1, Scale: 2}

func TestArgs(t *testing.T) {
	args := Args(region, "rec.mov")

	for flag, want := range map[string]string{
		"-f":              "avfoundation",
		"-capture_cursor": "1",
		"-framerate":      "30",
		"-i":              "Capture screen 1:none",
		"-vf":             "crop=1200:640:824:376",
		"-c:v":            "libx264",
		"-qp":             "0",
		"-pix_fmt":        "yuv444p",
		"-g":              "30",
	} {
		i := slices.Index(args, flag)
		if i < 0 || i+1 >= len(args) || args[i+1] != want {
			t.Errorf("%s: got %v, want %q", flag, args, want)
		}
	}
	if args[len(args)-1] != "rec.mov" {
		t.Errorf("output not last: %v", args)
	}
	// ffmpeg must read stdin, or Stop's "q" is ignored.
	if slices.Contains(args, "-nostdin") {
		t.Error("args include -nostdin")
	}
}

// fakeFFmpeg writes a shell script standing in for ffmpeg. The output path is
// passed as $1.
func fakeFFmpeg(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nout=\"$1\"\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func shortTimeouts(t *testing.T) {
	t.Helper()
	q, i := quitTimeout, interruptTimeout
	quitTimeout, interruptTimeout = 300*time.Millisecond, 300*time.Millisecond
	t.Cleanup(func() { quitTimeout, interruptTimeout = q, i })
}

func TestStopSendsQ(t *testing.T) {
	// Exits cleanly once it reads "q", like ffmpeg.
	ffmpeg := fakeFFmpeg(t, `read key; [ "$key" = q ] && echo video > "$out" && exit 0; exit 3`)
	out := filepath.Join(t.TempDir(), "rec.mov")

	rec, err := start(ffmpeg, []string{out}, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := rec.Stop(); err != nil {
		t.Errorf("second Stop: %v", err)
	}
}

func TestStopFallsBackToInterrupt(t *testing.T) {
	shortTimeouts(t)
	// Ignores stdin; writes its file and exits 255 on SIGINT, like ffmpeg.
	ffmpeg := fakeFFmpeg(t, `trap 'echo video > "$out"; exit 255' INT; while :; do sleep 0.05; done`)
	out := filepath.Join(t.TempDir(), "rec.mov")

	rec, err := start(ffmpeg, []string{out}, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Stop(); err != nil {
		t.Errorf("Stop: %v", err)
	}
}

func TestStopKillsStuckFFmpeg(t *testing.T) {
	shortTimeouts(t)
	ffmpeg := fakeFFmpeg(t, `trap '' INT; while :; do sleep 0.05; done`)
	out := filepath.Join(t.TempDir(), "rec.mov")

	rec, err := start(ffmpeg, []string{out}, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Stop(); err == nil || !strings.Contains(err.Error(), "did not stop") {
		t.Errorf("Stop = %v, want did-not-stop error", err)
	}
}

func TestFFmpegFailsOnItsOwn(t *testing.T) {
	ffmpeg := fakeFFmpeg(t, `echo "[AVFoundation indev] Cannot open Capture screen 3" >&2; exit 1`)
	out := filepath.Join(t.TempDir(), "rec.mov")

	rec, err := start(ffmpeg, []string{out}, out)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-rec.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("Done not closed")
	}
	if err := rec.Err(); err == nil || !strings.Contains(err.Error(), "Cannot open Capture screen 3") {
		t.Errorf("Err = %v, want ffmpeg's message", err)
	}
	if err := rec.Stop(); err == nil {
		t.Error("Stop after failure returned nil")
	}
}

func TestStopWithoutOutputFails(t *testing.T) {
	ffmpeg := fakeFFmpeg(t, `read key; exit 0`) // exits cleanly but writes nothing
	out := filepath.Join(t.TempDir(), "rec.mov")

	rec, err := start(ffmpeg, []string{out}, out)
	if err != nil {
		t.Fatal(err)
	}
	if err := rec.Stop(); err == nil || !strings.Contains(err.Error(), "no video") {
		t.Errorf("Stop = %v, want no-video error", err)
	}
}

func TestDiscard(t *testing.T) {
	ffmpeg := fakeFFmpeg(t, `echo partial > "$out"; while :; do sleep 0.05; done`)
	out := filepath.Join(t.TempDir(), "rec.mov")

	rec, err := start(ffmpeg, []string{out}, out)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond) // let it write
	rec.Discard()

	if _, err := os.Stat(out); !os.IsNotExist(err) {
		t.Errorf("output still exists after Discard: %v", err)
	}
	if err := rec.Stop(); err == nil {
		t.Error("Stop after Discard returned nil")
	}
}

// TestRecordAndEncodeWithRealFFmpeg records a live test pattern with the same
// output settings as a screen capture, stops it with "q", and checks that the
// encoder accepts the result.
func TestRecordAndEncodeWithRealFFmpeg(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping in -short mode")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg not installed")
	}
	gifsicle, err := exec.LookPath("gifsicle")
	if err != nil {
		t.Skip("gifsicle not installed")
	}

	out, dir, err := TempPath()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	// Swap the avfoundation input for a real-time test pattern.
	args := Args(region, out)
	in := slices.Index(args, "-f")
	end := slices.Index(args, "-i") + 2
	args = slices.Replace(args, in, end, "-re", "-f", "lavfi", "-i", "testsrc2=size=3024x1964:rate=30")

	rec, err := start(ffmpeg, args, out)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if err := rec.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	gif := filepath.Join(dir, "out.gif")
	res, err := encoder.Encode(context.Background(), encoder.Tools{FFmpeg: ffmpeg, Gifsicle: gifsicle}, out, gif, encoder.DefaultOptions, nil)
	if err != nil {
		t.Fatalf("encoding the recording: %v", err)
	}
	// Cropped to 1200×640, then scaled to the 800px default width.
	if res.Width != 800 || res.Height != 427 {
		t.Errorf("gif = %dx%d, want 800x427", res.Width, res.Height)
	}
	if res.Frames < 10 {
		t.Errorf("only %d frames from ~1.5s of recording", res.Frames)
	}
	t.Logf("recorded %s → %d frames, %d bytes", time.Since(rec.Started).Round(time.Millisecond), res.Frames, res.Size)
}
