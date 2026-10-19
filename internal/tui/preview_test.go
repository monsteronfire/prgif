package tui

import (
	"context"
	"errors"
	"image/gif"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/monsteronfire/prgif/internal/deps"
	"github.com/monsteronfire/prgif/internal/encoder"
)

var keyP = tea.KeyPressMsg{Code: 'p', Text: "p"}

// previewingModel presses p on Review and returns the model while the preview
// encodes, with the real encoder cancelled; tests send its result themselves.
func previewingModel(t *testing.T) Model {
	t.Helper()
	m := reviewModel(t)
	m, _ = update(t, m, done(m, someEstimate))

	m, cmd := update(t, m, keyP)

	if m.state != statePreviewing || cmd == nil || m.pendingPreview.dir == "" {
		t.Fatalf("state %v, pending %+v; want a preview encoding", m.state, m.pendingPreview)
	}
	if m.estimating {
		t.Error("estimate still running alongside the preview")
	}
	m.cancelEncode()
	t.Cleanup(func() { _ = os.RemoveAll(m.pendingPreview.dir) })
	return m
}

// previewedModel finishes a preview of size bytes and returns the model back
// on Review, with Quick Look calls recorded in calls.
func previewedModel(t *testing.T, size int64) (Model, *[]call) {
	t.Helper()
	calls := fakeRun(t, nil)
	m := previewingModel(t)
	path := filepath.Join(m.pendingPreview.dir, "preview.gif")
	if err := os.WriteFile(path, []byte("GIF89a preview"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := encoder.Result{Path: path, Size: size, Width: 800, Height: 427, Frames: 54, Duration: 4500 * time.Millisecond}

	m, cmd := update(t, m, encodeDoneMsg{result: res})

	if m.state != stateReview || !m.previewMatches() {
		t.Fatalf("state %v, preview %+v; want review with the preview", m.state, m.preview)
	}
	if cmd == nil {
		t.Fatal("Quick Look not opened")
	}
	m, _ = update(t, m, cmd())
	t.Cleanup(m.cleanupPreview)
	return m, calls
}

func TestPreviewOpensQuickLook(t *testing.T) {
	m, calls := previewedModel(t, 2_000_000)

	want := []string{"qlmanage", "-p", absPath(m.preview.result.Path)}
	if len(*calls) != 1 || !slices.Equal((*calls)[0].argv, want) {
		t.Errorf("calls = %+v, want %v", *calls, want)
	}
	out := render(m)
	for _, s := range []string{"2.0 MB (preview)", "Preview closed. Press enter to save it"} {
		if !strings.Contains(out, s) {
			t.Errorf("review view missing %q:\n%s", s, out)
		}
	}
}

func TestPreviewingView(t *testing.T) {
	m := previewingModel(t)
	m, _ = update(t, m, encodeProgressMsg{Stage: encoder.StageFrames, Fraction: 0.5})

	out := render(m)
	if !strings.Contains(out, "Making preview: encoding frames…") || !strings.Contains(out, "q/esc cancel") {
		t.Errorf("previewing view:\n%s", out)
	}
}

func TestEnterSavesPreviewWithoutEncoding(t *testing.T) {
	m, _ := previewedModel(t, 2_000_000)
	dir := m.preview.dir

	m, cmd := update(t, m, keyEnter)

	if m.state != stateDone {
		t.Fatalf("state = %v (err %v), want done", m.state, m.err)
	}
	if cmd != nil {
		t.Error("enter started work although the preview was up to date")
	}
	if got, err := os.ReadFile(m.output); err != nil || string(got) != "GIF89a preview" {
		t.Errorf("saved GIF = %q, %v; want the preview's contents", got, err)
	}
	if m.result.Path != m.output || m.result.Size != 2_000_000 {
		t.Errorf("result = %+v, want the preview's, at %s", m.result, m.output)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("preview dir not removed: %v", err)
	}
	if out := render(m); !strings.Contains(out, "GIF ready") || !strings.Contains(out, "00:04.5") {
		t.Errorf("done view:\n%s", out)
	}
}

func TestSettingChangeOutdatesPreview(t *testing.T) {
	m, _ := previewedModel(t, 2_000_000)
	dir := m.preview.dir

	m, _ = update(t, m, keyRight) // fps 12 → 15

	if m.previewMatches() || strings.Contains(render(m), "(preview)") {
		t.Errorf("preview still shown after a settings change:\n%s", render(m))
	}
	// Changing it back makes it current again.
	m, _ = update(t, m, keyLeft)
	if !m.previewMatches() {
		t.Error("preview not current after restoring the setting")
	}

	// Saving with other settings encodes afresh and drops the old preview.
	m, _ = update(t, m, keyRight)
	m, _ = update(t, m, keyEnter)
	if m.state != stateEncoding {
		t.Fatalf("state = %v, want encoding", m.state)
	}
	m.cancelEncode()
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("outdated preview not removed: %v", err)
	}
}

func TestPreviewAgainReopensWithoutEncoding(t *testing.T) {
	m, calls := previewedModel(t, 2_000_000)

	m, cmd := update(t, m, keyP)

	if m.state != stateReview || cmd == nil {
		t.Fatalf("state %v; want review, reopening Quick Look", m.state)
	}
	cmd()
	if len(*calls) != 2 || (*calls)[1].argv[0] != "qlmanage" {
		t.Errorf("calls = %+v, want Quick Look opened again", *calls)
	}
}

func TestCancelPreview(t *testing.T) {
	m := previewingModel(t)
	dir := m.pendingPreview.dir

	m, _ = update(t, m, keyEsc)
	if m.state != statePreviewing || !strings.Contains(render(m), "Cancelling preview") {
		t.Fatalf("state %v; want previewing until the encoder stops:\n%s", m.state, render(m))
	}

	m, cmd := update(t, m, encodeDoneMsg{err: context.Canceled})

	if m.state != stateReview || m.notice != "" {
		t.Errorf("state %v, notice %q; want review, quietly", m.state, m.notice)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("cancelled preview not removed: %v", err)
	}
	if cmd == nil || !m.estimating {
		t.Error("estimate not restarted after the preview")
	}
	m.stopEstimate()
}

func TestPreviewFailure(t *testing.T) {
	m := previewingModel(t)

	m, _ = update(t, m, encodeDoneMsg{err: errors.New("encoding frames: ffmpeg: exit status 1\nlots of ffmpeg output")})

	if m.state != stateReview {
		t.Fatalf("state = %v, want review", m.state)
	}
	if out := render(m); !strings.Contains(out, "Preview failed: encoding frames: ffmpeg: exit status 1") || strings.Contains(out, "lots of") {
		t.Errorf("failure notice:\n%s", out)
	}
	m.stopEstimate()
}

func TestQuitFromReviewDeletesPreview(t *testing.T) {
	m, _ := previewedModel(t, 2_000_000)
	dir := m.preview.dir

	_, cmd := update(t, m, keyQ)

	if !isQuit(cmd) {
		t.Error("q did not quit")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("preview not removed: %v", err)
	}
}

func TestMoveFile(t *testing.T) {
	dir := t.TempDir()
	src, dst := filepath.Join(dir, "a.gif"), filepath.Join(dir, "b.gif")
	if err := os.WriteFile(src, []byte("gif"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := moveFile(src, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "gif" {
		t.Errorf("dst = %q", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Error("src left behind")
	}
	if err := moveFile(filepath.Join(dir, "missing.gif"), dst); err == nil {
		t.Error("no error moving a missing file")
	}
}

// TestPreviewAndSaveRealGIF previews a sped-up clip with the real ffmpeg and
// gifsicle, then saves it, and checks the saved GIF is the preview.
func TestPreviewAndSaveRealGIF(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	ffmpeg, err1 := exec.LookPath("ffmpeg")
	gifsicle, err2 := exec.LookPath("gifsicle")
	if err1 != nil || err2 != nil {
		t.Skip("ffmpeg or gifsicle not installed")
	}
	in := filepath.Join(t.TempDir(), "clip.mov")
	if out, err := exec.Command(ffmpeg, "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=duration=6:size=640x360:rate=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-qp", "0", "-pix_fmt", "yuv444p", "-g", "30", in,
	).CombinedOutput(); err != nil {
		t.Fatalf("making test clip: %v\n%s", err, out)
	}
	fakeRun(t, nil) // no Quick Look here
	opts := encoder.DefaultOptions
	opts.Speed = 2
	out := filepath.Join(t.TempDir(), "out.gif")
	m := New(deps.Report{{Tool: deps.Required[0], Path: ffmpeg}, {Tool: deps.Required[1], Path: gifsicle}},
		Config{Encoder: opts, EncodeFile: in, Output: out})
	m, _ = update(t, m, m.Init()())
	m.stopEstimate()

	m, _ = update(t, m, keyP)
	for m.state == statePreviewing {
		m, _ = update(t, m, <-m.encodeCh)
	}
	if !m.previewMatches() {
		t.Fatalf("no preview: %s", m.notice)
	}
	preview := m.preview.result
	m, _ = update(t, m, keyEnter)

	if m.state != stateDone {
		t.Fatalf("state = %v (err %v), want done", m.state, m.err)
	}
	info, err := os.Stat(out)
	if err != nil || info.Size() != preview.Size {
		t.Fatalf("saved GIF: %v, size %v; want the preview's %d bytes", err, info, preview.Size)
	}
	f, _ := os.Open(out)
	defer func() { _ = f.Close() }() // read-only
	if _, err := gif.DecodeConfig(f); err != nil {
		t.Errorf("not a valid GIF: %v", err)
	}
	// 6s at 2× plays for 3s.
	if m.result.Duration != 3*time.Second || !strings.Contains(render(m), "00:03.0 · 2× speed") {
		t.Errorf("duration %v; done view:\n%s", m.result.Duration, render(m))
	}
}
