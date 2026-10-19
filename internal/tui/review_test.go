package tui

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/monsteronfire/prgif/internal/deps"
	"github.com/monsteronfire/prgif/internal/encoder"
	"github.com/monsteronfire/prgif/internal/selector"
)

var keyT = tea.KeyPressMsg{Code: 't', Text: "t"}

// reviewModel returns a model on the Review screen for a fake recording,
// with its first estimate started but not run.
func reviewModel(t *testing.T) Model {
	t.Helper()
	dir := t.TempDir()
	m := New(allTools, Config{Encoder: encoder.DefaultOptions, Output: filepath.Join(t.TempDir(), "out.gif")})
	m.region = selector.Region{W: 1200, H: 640, Scale: 2}
	m.recordDir = dir
	m.recordPath = filepath.Join(dir, "recording.mov")
	if err := os.WriteFile(m.recordPath, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}

	next, cmd := m.startReview(m.recordPath)
	m = next.(Model)
	if m.state != stateReview || cmd == nil || !m.estimating {
		t.Fatalf("state %v, estimating %v, cmd %v; want review with an estimate running", m.state, m.estimating, cmd)
	}
	t.Cleanup(m.stopEstimate)
	return m
}

// done finishes the latest estimate with est, as if it had run.
func done(m Model, est encoder.SizeEstimate) estimateDoneMsg {
	return estimateDoneMsg{seq: m.estimateSeq, full: 5 * time.Second, estimate: est}
}

var someEstimate = encoder.SizeEstimate{Bytes: 3_400_000, Low: 2_100_000, High: 4_800_000, Width: 800, Height: 427}

func TestReviewShowsEstimate(t *testing.T) {
	m := reviewModel(t)
	if out := render(m); !strings.Contains(out, "estimating…") || !strings.Contains(out, "measuring…") {
		t.Errorf("review view before the estimate:\n%s", out)
	}

	m, _ = update(t, m, done(m, someEstimate))

	if m.estimating || m.clipDuration != 5*time.Second {
		t.Errorf("estimating %v, clip %v; want done with the probed 5s", m.estimating, m.clipDuration)
	}
	out := render(m)
	for _, want := range []string{
		"≈ 3.4 MB (2.1–4.8 MB)",
		"00:04.5 · 0.5s trimmed from the end",
		"800×427",
		"enter save", "p preview", "esc discard",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("review view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "GitHub") {
		t.Errorf("size warning for a small estimate:\n%s", out)
	}
}

func TestReviewExactEstimate(t *testing.T) {
	m := reviewModel(t)
	m, _ = update(t, m, done(m, encoder.SizeEstimate{Bytes: 355_328, Low: 355_328, High: 355_328, Exact: true}))

	if out := render(m); !strings.Contains(out, "355.3 KB") || strings.Contains(out, "≈") {
		t.Errorf("exact estimate not shown plainly:\n%s", out)
	}
}

func TestReviewTrimUpdatesClipLength(t *testing.T) {
	m := reviewModel(t)
	m, _ = update(t, m, done(m, someEstimate))
	m.setting = settingTrim

	m, _ = update(t, m, keyRight) // 0.5s → 0.75s

	if out := render(m); !strings.Contains(out, "00:04.3 · 0.75s trimmed from the end") {
		t.Errorf("clip length not updated for the new trim:\n%s", out)
	}
}

func TestReviewDropsStaleEstimates(t *testing.T) {
	m := reviewModel(t)
	first := m.estimateSeq

	// Two quick changes: each restarts the debounce.
	m, cmd := update(t, m, keyRight)
	if cmd == nil || !m.estimating {
		t.Fatal("setting change did not schedule an estimate")
	}
	m, _ = update(t, m, keyRight)
	if m.cfg.Encoder.FPS != 20 {
		t.Fatalf("fps = %d, want 20", m.cfg.Encoder.FPS)
	}

	// The first estimate (for 12 fps) finishes late: ignored.
	m, _ = update(t, m, estimateDoneMsg{seq: first, full: 5 * time.Second, estimate: someEstimate})
	if !m.estimating || m.estimate.Bytes != 0 || m.clipDuration != 0 {
		t.Errorf("stale estimate applied: %+v", m.estimate)
	}

	// The first change's debounce ends: superseded, so nothing starts.
	if _, cmd = update(t, m, estimateDueMsg{seq: m.estimateSeq - 1}); cmd != nil {
		t.Error("superseded debounce started an estimate")
	}

	// The latest debounce ends: the estimate starts, and its result applies.
	m, cmd = update(t, m, estimateDueMsg{seq: m.estimateSeq})
	if cmd == nil {
		t.Fatal("latest debounce did not start an estimate")
	}
	m, _ = update(t, m, done(m, encoder.SizeEstimate{Bytes: 5_000_000, Low: 4_000_000, High: 6_000_000}))
	if out := render(m); !strings.Contains(out, "≈ 5.0 MB (4.0–6.0 MB)") {
		t.Errorf("latest estimate not shown:\n%s", out)
	}
}

func TestReviewChangeCancelsRunningEstimate(t *testing.T) {
	m := reviewModel(t)
	cancelled := false
	m.cancelEstimate = func() { cancelled = true }

	m, _ = update(t, m, keyRight)

	if !cancelled {
		t.Error("running estimate not cancelled by a settings change")
	}
}

func TestReviewKeepsPreviousEstimateWhileRecalculating(t *testing.T) {
	m := reviewModel(t)
	m, _ = update(t, m, done(m, someEstimate))

	m, _ = update(t, m, keyRight)

	if out := render(m); !strings.Contains(out, "≈ 3.4 MB") || strings.Contains(out, "estimating…") {
		t.Errorf("previous estimate not kept while recalculating:\n%s", out)
	}
}

func TestReviewShuffleReestimates(t *testing.T) {
	m := reviewModel(t)
	m, _ = update(t, m, done(m, someEstimate))
	m.cfg.Encoder.Background.Padding = 8
	seed, seq := m.cfg.Encoder.Background.Seed, m.estimateSeq

	m, cmd := update(t, m, keyG)

	if m.cfg.Encoder.Background.Seed == seed || cmd == nil || m.estimateSeq == seq || !m.estimating {
		t.Error("shuffling the background didn't change the colours and re-estimate")
	}
}

func TestReviewNoChangeAtEndOfChoices(t *testing.T) {
	m := reviewModel(t)
	m, _ = update(t, m, done(m, someEstimate))
	m.cfg.Encoder.FPS = fpsChoices[len(fpsChoices)-1]
	seq := m.estimateSeq

	m, cmd := update(t, m, keyRight)

	if cmd != nil || m.estimateSeq != seq || m.estimating {
		t.Error("re-estimated although the setting didn't change")
	}
}

func TestReviewOverGitHubLimit(t *testing.T) {
	m := reviewModel(t)
	m, _ = update(t, m, done(m, encoder.SizeEstimate{Bytes: 14_000_000, Low: 11_000_000, High: 17_000_000}))

	if out := render(m); !strings.Contains(out, "Likely over GitHub's 10.0 MB image limit") {
		t.Errorf("no size warning:\n%s", out)
	}
}

func TestReviewEstimateFailure(t *testing.T) {
	m := reviewModel(t)
	m, _ = update(t, m, estimateDoneMsg{seq: m.estimateSeq, err: errors.New("ffmpeg: exit status 1")})

	if m.state != stateReview {
		t.Fatalf("state = %v, want review", m.state)
	}
	if out := render(m); !strings.Contains(out, "estimate unavailable") {
		t.Errorf("no failure note:\n%s", out)
	}
	m, _ = update(t, m, keyEnter)
	if m.state != stateEncoding {
		t.Errorf("state after enter = %v, want encoding", m.state)
	}
	m.cancelEncode()
}

func TestReviewEnterEncodes(t *testing.T) {
	m := reviewModel(t)
	cancelled := false
	m.cancelEstimate = func() { cancelled = true }

	m, _ = update(t, m, keyEnter)

	if m.state != stateEncoding {
		t.Fatalf("state = %v, want encoding", m.state)
	}
	m.cancelEncode()
	if !cancelled {
		t.Error("estimate still running while encoding")
	}
	// A late estimate must not disturb encoding.
	if m, _ = update(t, m, done(m, someEstimate)); m.state != stateEncoding {
		t.Errorf("late estimate changed state to %v", m.state)
	}
}

func TestReviewDiscard(t *testing.T) {
	m := reviewModel(t)
	dir := m.recordDir

	m, _ = update(t, m, keyEsc)

	if m.state != stateReady {
		t.Errorf("state = %v, want ready", m.state)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("recording not deleted: %v", err)
	}
	if out := render(m); !strings.Contains(out, "Recording discarded") {
		t.Errorf("no discarded notice:\n%s", out)
	}
}

func TestReviewQuitDeletesRecording(t *testing.T) {
	m := reviewModel(t)
	dir := m.recordDir

	_, cmd := update(t, m, keyQ)

	if !isQuit(cmd) {
		t.Error("q did not quit")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("recording not deleted: %v", err)
	}
}

func TestReviewEncodeFileHasNoTrim(t *testing.T) {
	in := filepath.Join(t.TempDir(), "clip.mov")
	if err := os.WriteFile(in, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(allTools, Config{Encoder: encoder.DefaultOptions, EncodeFile: in})
	m, _ = update(t, m, m.Init()())
	t.Cleanup(m.stopEstimate)
	if m.state != stateReview {
		t.Fatalf("state = %v, want review", m.state)
	}
	m, _ = update(t, m, done(m, someEstimate))

	out := render(m)
	if strings.Contains(out, "trim") || strings.Contains(out, "trimmed") {
		t.Errorf("trim shown for an --encode input:\n%s", out)
	}
	if !strings.Contains(out, "00:05.0") || !strings.Contains(out, "800×427") {
		t.Errorf("review view lacks untrimmed length or estimated size:\n%s", out)
	}
	// ↓ cycles fps → max width → lossy → speed → background → fps, skipping trim.
	for _, want := range []setting{settingWidth, settingLossy, settingSpeed, settingBackground, settingFPS} {
		if m, _ = update(t, m, keyDown); m.setting != want {
			t.Errorf("setting = %v, want %v", m.setting, want)
		}
	}

	// esc leaves the user's file alone.
	m, _ = update(t, m, keyEsc)
	if _, err := os.Stat(in); err != nil {
		t.Errorf("--encode input deleted: %v", err)
	}
	if m.state != stateIdle {
		t.Errorf("state = %v, want idle", m.state)
	}
}

func TestDoneTweakReturnsToReview(t *testing.T) {
	m := reviewModel(t)
	m, _ = update(t, m, done(m, someEstimate))
	m, _ = update(t, m, keyEnter)
	m.cancelEncode()
	m, _ = update(t, m, encodeDoneMsg{result: encoder.Result{Path: "out.gif", Size: 12_000_000, Frames: 1}})
	if out := render(m); !strings.Contains(out, "Press t to re-encode") {
		t.Errorf("done view lacks the re-encode hint:\n%s", out)
	}

	m, cmd := update(t, m, keyT)

	if m.state != stateReview || cmd == nil || !m.estimating {
		t.Fatalf("state %v, estimating %v; want review with a new estimate", m.state, m.estimating)
	}
	t.Cleanup(m.stopEstimate)
	if m.clipDuration != 5*time.Second {
		t.Errorf("clip length re-probed: %v", m.clipDuration)
	}
	if _, err := os.Stat(m.recordPath); err != nil {
		t.Errorf("recording gone: %v", err)
	}
}

func TestRecordAgainDeletesRecording(t *testing.T) {
	m := reviewModel(t)
	dir := m.recordDir
	m, _ = update(t, m, keyEnter)
	m.cancelEncode()
	m, _ = update(t, m, encodeDoneMsg{result: encoder.Result{Path: "out.gif", Frames: 1}})

	m, _ = update(t, m, keyR)

	if m.state != stateReady {
		t.Errorf("state = %v, want ready", m.state)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("recording not deleted: %v", err)
	}
}

// TestReviewRealEstimate runs the estimate command with the real ffmpeg and
// gifsicle, from probing the clip to showing the result.
func TestReviewRealEstimate(t *testing.T) {
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
		"-f", "lavfi", "-i", "testsrc=duration=6:size=1200x640:rate=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-qp", "0", "-pix_fmt", "yuv444p", "-g", "30", in,
	).CombinedOutput(); err != nil {
		t.Fatalf("making test clip: %v\n%s", err, out)
	}
	tools := deps.Report{
		{Tool: deps.Required[0], Path: ffmpeg},
		{Tool: deps.Required[1], Path: gifsicle},
	}
	m := New(tools, Config{Encoder: encoder.DefaultOptions, EncodeFile: in})

	m, cmd := update(t, m, m.Init()())
	m, _ = update(t, m, runUntil[estimateDoneMsg](t, cmd))

	if m.estimateErr != nil {
		t.Fatal(m.estimateErr)
	}
	out := render(m)
	t.Log("\n" + out)
	if m.clipDuration != 6*time.Second || !strings.Contains(out, "≈ ") || !strings.Contains(out, "800×427") {
		t.Errorf("clip %v; review view lacks the estimate:\n%s", m.clipDuration, out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if w := ansi.StringWidth(line); w > contentWidth+8 { // + border and padding
			t.Errorf("line %d wide:\n%s", w, line)
		}
	}
}

func TestFormatEstimate(t *testing.T) {
	tests := []struct {
		est  encoder.SizeEstimate
		want string
	}{
		{encoder.SizeEstimate{Bytes: 3_400_000, Low: 2_100_000, High: 4_800_000}, "≈ 3.4 MB (2.1–4.8 MB)"},
		{encoder.SizeEstimate{Bytes: 950_000, Low: 800_000, High: 1_200_000}, "≈ 950.0 KB (800.0 KB–1.2 MB)"},
		{encoder.SizeEstimate{Bytes: 976, Low: 976, High: 976, Exact: true}, "976 B"},
	}
	for _, tt := range tests {
		if got := formatEstimate(tt.est); got != tt.want {
			t.Errorf("formatEstimate(%+v) = %q, want %q", tt.est, got, tt.want)
		}
	}
}
