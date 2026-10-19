package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/monsteronfire/prgif/internal/deps"
	"github.com/monsteronfire/prgif/internal/encoder"
	"github.com/monsteronfire/prgif/internal/selector"
)

var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keySpace = tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
	keyQ     = tea.KeyPressMsg{Code: 'q', Text: "q"}
	keyCtrlC = tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
)

// Stand-ins for ffmpeg. The output path is the last argument.
const (
	ffmpegRecords = `for a; do out="$a"; done; read key; echo video > "$out"; exit 0`
	ffmpegFails   = `echo "[AVFoundation indev] Cannot open Capture screen 0" >&2; exit 1`
	ffmpegRuns    = `while :; do sleep 0.05; done`
)

// readyModel returns a model on the Ready screen whose ffmpeg is script.
func readyModel(t *testing.T, script string) Model {
	t.Helper()
	ffmpeg := filepath.Join(t.TempDir(), "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := deps.Report{
		{Tool: deps.Required[0], Path: ffmpeg},
		{Tool: deps.Required[1], Path: "/usr/bin/gifsicle"},
		{Tool: deps.Required[2], Path: "/usr/bin/select-region"},
	}
	m := New(report, Config{Encoder: encoder.DefaultOptions, Output: filepath.Join(t.TempDir(), "out.gif")})
	m.state = stateReady
	m.region = selector.Region{X: 0, Y: 0, W: 640, H: 480, ScreenIndex: 0, Scale: 2}
	return m
}

// recordingModel counts down from Ready and returns the model once recording
// has started, with the command that started it.
func recordingModel(t *testing.T, script string) (Model, tea.Cmd) {
	t.Helper()
	m := readyModel(t, script)

	m, _ = update(t, m, keyEnter)
	if m.state != stateCountdown {
		t.Fatalf("state after enter = %v, want countdown", m.state)
	}
	if out := render(m); !strings.Contains(out, "Recording starts in") || !strings.Contains(out, "3") {
		t.Errorf("countdown view:\n%s", out)
	}

	var cmd tea.Cmd
	for range countdownFrom {
		m, cmd = update(t, m, countdownTickMsg{})
	}
	if m.state != stateRecording {
		t.Fatalf("state after countdown = %v, want recording", m.state)
	}
	t.Cleanup(func() { m.discardRecording() })
	return m, cmd
}

func TestRecordStopAndEncode(t *testing.T) {
	m, _ := recordingModel(t, ffmpegRecords)
	dir := m.recordDir
	if out := render(m); !strings.Contains(out, "REC") || !strings.Contains(out, "stop & make GIF") {
		t.Errorf("recording view:\n%s", out)
	}

	// q must not stop or discard a take.
	if m, _ = update(t, m, keyQ); m.state != stateRecording {
		t.Fatalf("q changed state to %v", m.state)
	}

	m, cmd := update(t, m, keySpace)
	if m.state != stateStopping {
		t.Fatalf("state after space = %v, want stopping", m.state)
	}
	m, _ = update(t, m, runUntil[recordingStoppedMsg](t, cmd))

	// Stopping opens Review, which starts estimating (not run here).
	if m.state != stateReview {
		t.Fatalf("state after stop = %v (err %v), want review", m.state, m.err)
	}
	if !m.estimating || m.input != m.recordPath {
		t.Errorf("review: estimating %v, input %q; want an estimate of %q", m.estimating, m.input, m.recordPath)
	}

	m, _ = update(t, m, keyEnter)
	if m.state != stateEncoding {
		t.Fatalf("state after enter = %v (err %v), want encoding", m.state, m.err)
	}
	m.cancelEncode() // the fake gifsicle path would fail anyway
	if _, err := os.Stat(filepath.Join(dir, "recording.mov")); err != nil {
		t.Errorf("recording missing while encoding: %v", err)
	}

	// A successful encode keeps the recording, so it can be re-encoded.
	res := encoder.Result{Path: "out.gif", Frames: 1, Duration: 2500 * time.Millisecond, Trimmed: 500 * time.Millisecond}
	m, _ = update(t, m, encodeDoneMsg{result: res})
	if m.state != stateDone {
		t.Fatalf("state = %v, want done", m.state)
	}
	if _, err := os.Stat(dir); err != nil {
		t.Errorf("recording deleted after encoding: %v", err)
	}
	if out := render(m); !strings.Contains(out, "00:02.5 · 0.5s trimmed from the end") {
		t.Errorf("done view lacks clip length:\n%s", out)
	}

	// Quitting deletes it.
	if _, cmd = update(t, m, keyQ); !isQuit(cmd) {
		t.Error("q did not quit")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp recording dir not removed on quit: %v", err)
	}
}

func TestEncodeFailureKeepsRecording(t *testing.T) {
	m, _ := recordingModel(t, ffmpegRecords)
	m, cmd := update(t, m, keyEnter)
	m, _ = update(t, m, runUntil[recordingStoppedMsg](t, cmd))
	m, _ = update(t, m, keyEnter) // Review → Encoding
	m.cancelEncode()
	rec := m.recordPath

	m, _ = update(t, m, encodeDoneMsg{err: errors.New("boom")})

	if m.state != stateError {
		t.Fatalf("state = %v, want error", m.state)
	}
	if _, err := os.Stat(rec); err != nil {
		t.Errorf("recording deleted after a failed encode: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(filepath.Dir(rec)) })
	out := render(m)
	if !strings.Contains(out, "prgif --encode") || !strings.Contains(out, "recording.mov") {
		t.Errorf("error view lacks retry hint:\n%s", out)
	}
}

func TestDiscardRecording(t *testing.T) {
	m, _ := recordingModel(t, ffmpegRuns)
	dir := m.recordDir

	m, _ = update(t, m, keyEsc)

	if m.state != stateReady {
		t.Errorf("state = %v, want ready", m.state)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp recording dir not removed: %v", err)
	}
	if out := render(m); !strings.Contains(out, "Recording discarded") {
		t.Errorf("no discarded notice:\n%s", out)
	}
}

func TestCtrlCWhileRecordingDiscardsAndQuits(t *testing.T) {
	m, _ := recordingModel(t, ffmpegRuns)
	dir := m.recordDir

	_, cmd := update(t, m, keyCtrlC)

	if !isQuit(cmd) {
		t.Error("ctrl+c did not quit")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp recording dir not removed: %v", err)
	}
}

func TestFFmpegFailureDuringRecording(t *testing.T) {
	m, cmd := recordingModel(t, ffmpegFails)
	dir := m.recordDir

	m, _ = update(t, m, runUntil[recorderExitedMsg](t, cmd))

	if m.state != stateError {
		t.Fatalf("state = %v, want error", m.state)
	}
	if out := render(m); !strings.Contains(out, "Cannot open Capture screen 0") {
		t.Errorf("error view lacks ffmpeg message:\n%s", out)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("temp recording dir not removed: %v", err)
	}
}

func TestCancelCountdown(t *testing.T) {
	m := readyModel(t, ffmpegRecords)
	m, _ = update(t, m, keyEnter)

	m, _ = update(t, m, keyEsc)
	if m.state != stateReady {
		t.Fatalf("state = %v, want ready", m.state)
	}
	// The tick already in flight must not start a recording.
	for range countdownFrom {
		m, _ = update(t, m, countdownTickMsg{})
	}
	if m.state != stateReady || m.recorder != nil {
		t.Errorf("recording started after cancel: state %v", m.state)
	}
}

func TestRecordDisabledWithoutFFmpeg(t *testing.T) {
	m := New(deps.Report{{Tool: deps.Required[0]}, {Tool: deps.Required[1]}}, Config{Encoder: encoder.DefaultOptions})
	m.state = stateReady
	m.region = selector.Region{W: 640, H: 480, Scale: 1}

	m, _ = update(t, m, keyEnter)

	if m.state != stateReady {
		t.Errorf("enter changed state to %v without ffmpeg", m.state)
	}
	out := render(m)
	if strings.Contains(out, "enter record") || !strings.Contains(out, "Install ffmpeg and gifsicle") {
		t.Errorf("ready view without ffmpeg:\n%s", out)
	}
}

func TestFormatClock(t *testing.T) {
	tests := map[time.Duration]string{
		0:                      "00:00.0",
		7*time.Second + 340e6:  "00:07.3",
		67*time.Second + 960e6: "01:08.0",
		10*time.Minute + 5e8:   "10:00.5",
	}
	for d, want := range tests {
		if got := formatClock(d); got != want {
			t.Errorf("formatClock(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestTrimOnlyAppliesToRecordings(t *testing.T) {
	// --encode input: no recordPath, so no trimming.
	m := New(allTools, Config{Encoder: encoder.DefaultOptions, Output: filepath.Join(t.TempDir(), "a.gif")})
	if got := m.encodeOptions(); got.TrimEnd != 0 {
		t.Errorf("--encode TrimEnd = %v, want 0", got.TrimEnd)
	}

	m.recordPath = "/tmp/prgif-rec-x/recording.mov"
	if got := m.encodeOptions(); got.TrimEnd != encoder.DefaultOptions.TrimEnd {
		t.Errorf("recording TrimEnd = %v, want %v", got.TrimEnd, encoder.DefaultOptions.TrimEnd)
	}
}

// withFrameHelper gives m a select-region stand-in that can draw a frame. It
// writes its first argument to the returned file, then waits for stdin to
// close, like the real helper.
func withFrameHelper(t *testing.T, m Model) (Model, string) {
	t.Helper()
	dir := t.TempDir()
	started := filepath.Join(dir, "started")
	helper := filepath.Join(dir, "select-region")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho \"$1\" > "+started+"\ncat > /dev/null\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.deps[2].Path = helper
	m.region.CanFrame = true
	return m, started
}

func TestCountdownShowsFrame(t *testing.T) {
	m, started := withFrameHelper(t, readyModel(t, ffmpegRuns))

	m, _ = update(t, m, keyEnter)
	if m.frame == nil {
		t.Fatal("no frame shown during the countdown")
	}
	var got []byte
	for deadline := time.Now().Add(5 * time.Second); len(got) == 0 && time.Now().Before(deadline); {
		got, _ = os.ReadFile(started)
		time.Sleep(10 * time.Millisecond)
	}
	if string(got) != "--frame\n" {
		t.Errorf("helper started with %q, want --frame", got)
	}

	m, _ = update(t, m, keyEsc)
	if m.frame != nil || m.state != stateReady {
		t.Errorf("frame %v, state %v after cancelling the countdown; want hidden, ready", m.frame, m.state)
	}
}

func TestFrameStaysUntilStop(t *testing.T) {
	m, _ := withFrameHelper(t, readyModel(t, ffmpegRecords))
	m, _ = update(t, m, keyEnter)
	for range countdownFrom {
		m, _ = update(t, m, countdownTickMsg{})
	}
	t.Cleanup(func() { m.discardRecording() })
	if m.state != stateRecording || m.frame == nil {
		t.Fatalf("state %v, frame %v; want recording with the frame shown", m.state, m.frame)
	}
	m, _ = update(t, m, keyEnter)
	if m.state != stateStopping || m.frame != nil {
		t.Errorf("state %v, frame %v after stop; want stopping with the frame hidden", m.state, m.frame)
	}
}

func TestNoFrameFromOldHelper(t *testing.T) {
	m := readyModel(t, ffmpegRuns) // region.CanFrame is false
	m, _ = update(t, m, keyEnter)
	if m.frame != nil {
		t.Error("frame shown although the helper can't draw one")
	}
}
