package tui

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/monsteronfire/prgif/internal/deps"
	"github.com/monsteronfire/prgif/internal/encoder"
	"github.com/monsteronfire/prgif/internal/selector"
)

type call struct {
	stdin string
	argv  []string
}

// fakeRun replaces runCommand, recording calls and returning err.
func fakeRun(t *testing.T, err error) *[]call {
	t.Helper()
	var calls []call
	orig := runCommand
	runCommand = func(stdin, name string, args ...string) error {
		calls = append(calls, call{stdin: stdin, argv: append([]string{name}, args...)})
		return err
	}
	t.Cleanup(func() { runCommand = orig })
	return &calls
}

func doneModel(t *testing.T) Model {
	t.Helper()
	m, _, _ := encodingModel()
	m, _ = update(t, m, encodeDoneMsg{result: encoder.Result{Path: "demo.gif", Size: 1000, Width: 10, Height: 10, Frames: 1}})
	if m.state != stateDone {
		t.Fatalf("state = %v, want done", m.state)
	}
	return m
}

// press sends key k, runs the resulting command and feeds its message back.
func press(t *testing.T, m Model, k tea.KeyPressMsg) Model {
	t.Helper()
	m, cmd := update(t, m, k)
	if cmd != nil {
		m, _ = update(t, m, cmd())
	}
	return m
}

func TestDoneRevealInFinder(t *testing.T) {
	calls := fakeRun(t, nil)

	m := press(t, doneModel(t), tea.KeyPressMsg{Code: 'o', Text: "o"})

	want := []string{"open", "-R", absPath("demo.gif")}
	if len(*calls) != 1 || !slices.Equal((*calls)[0].argv, want) {
		t.Errorf("calls = %+v, want %v", *calls, want)
	}
	if !strings.Contains(render(m), "Revealed in Finder") {
		t.Errorf("no notice:\n%s", render(m))
	}
}

func TestDoneCopyPath(t *testing.T) {
	calls := fakeRun(t, nil)

	m := press(t, doneModel(t), tea.KeyPressMsg{Code: 'c', Text: "c"})

	if len(*calls) != 1 || (*calls)[0].argv[0] != "pbcopy" || (*calls)[0].stdin != absPath("demo.gif") {
		t.Errorf("calls = %+v, want pbcopy with the absolute path", *calls)
	}
	if !strings.Contains(render(m), "Copied path to clipboard") {
		t.Errorf("no notice:\n%s", render(m))
	}
}

func TestDoneActionFailure(t *testing.T) {
	fakeRun(t, errors.New("pbcopy: not found"))

	m := press(t, doneModel(t), tea.KeyPressMsg{Code: 'c', Text: "c"})

	if out := render(m); !strings.Contains(out, "Couldn't copy the path: pbcopy: not found") {
		t.Errorf("no failure notice:\n%s", out)
	}
	if m.state != stateDone {
		t.Errorf("state = %v, want done", m.state)
	}
}

func TestDoneHelpListsActions(t *testing.T) {
	out := render(doneModel(t))
	for _, want := range []string{"o reveal", "copy path", "t tweak", "record again", "quit"} {
		if !strings.Contains(out, want) {
			t.Errorf("help missing %q:\n%s", want, out)
		}
	}
}

var keyR = tea.KeyPressMsg{Code: 'r', Text: "r"}

func TestRecordAgainKeepsRegionAndSettings(t *testing.T) {
	m := doneModel(t)
	m.region = selector.Region{W: 640, H: 480, Scale: 2}
	m.cfg.Encoder.FPS = 20

	m, _ = update(t, m, keyR)

	if m.state != stateReady || m.region.W != 640 || m.cfg.Encoder.FPS != 20 {
		t.Errorf("state=%v region=%+v fps=%d, want ready with region and fps kept", m.state, m.region, m.cfg.Encoder.FPS)
	}
}

func TestRecordAgainWithoutRegion(t *testing.T) {
	// After --encode there's no region: select one, if the helper exists.
	m := doneModel(t)
	if m, _ = update(t, m, keyR); m.state != stateSelecting {
		t.Errorf("state = %v, want selecting", m.state)
	}
	m.cancelSelect()

	m = doneModel(t)
	m.keys.Select.SetEnabled(false)
	if m, _ = update(t, m, keyR); m.state != stateIdle {
		t.Errorf("without helper: state = %v, want idle", m.state)
	}
}

func TestSecondGIFDoesNotOverwriteOutput(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "demo.gif")
	if err := os.WriteFile(out, []byte("first"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(allTools, Config{Encoder: encoder.DefaultOptions, Output: out})

	// The first GIF of a session goes exactly where -o says, even if it exists.
	next, _ := m.startEncoding("in.mov")
	m = next.(Model)
	m.cancelEncode()
	if m.output != out {
		t.Errorf("first output = %q, want %q", m.output, out)
	}

	m.gifsMade = 1
	next, _ = m.startEncoding("in.mov")
	m = next.(Model)
	m.cancelEncode()
	if want := filepath.Join(dir, "demo-2.gif"); m.output != want {
		t.Errorf("second output = %q, want %q", m.output, want)
	}
}

func TestAvailablePath(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.gif")
	if got := availablePath(p); got != p {
		t.Errorf("free path changed to %q", got)
	}
	for _, name := range []string{"x.gif", "x-2.gif"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := availablePath(p), filepath.Join(dir, "x-3.gif"); got != want {
		t.Errorf("availablePath = %q, want %q", got, want)
	}
}

func TestPermissionHint(t *testing.T) {
	calls := fakeRun(t, nil)
	m, cmd := recordingModel(t, ffmpegFails) // "[AVFoundation indev] Cannot open Capture screen 0"
	m, _ = update(t, m, runUntil[recorderExitedMsg](t, cmd))

	if !m.permissionHint {
		t.Fatal("no permission hint for an avfoundation error")
	}
	out := render(m)
	for _, want := range []string{"Screen Recording", "open privacy settings"} {
		if !strings.Contains(out, want) {
			t.Errorf("error view missing %q:\n%s", want, out)
		}
	}

	press(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if len(*calls) != 1 || !slices.Equal((*calls)[0].argv, []string{"open", screenRecordingSettings}) {
		t.Errorf("calls = %+v, want open settings", *calls)
	}
}

func TestNoPermissionHintForOtherErrors(t *testing.T) {
	calls := fakeRun(t, nil)
	m := New(deps.Report{}, Config{})
	m, _ = update(t, m, reviewMsg{input: "x.mov"}) // fails: no ffmpeg

	if m.permissionHint || strings.Contains(render(m), "Screen Recording") {
		t.Errorf("permission hint shown for an unrelated error:\n%s", render(m))
	}
	press(t, m, tea.KeyPressMsg{Code: 'p', Text: "p"})
	if len(*calls) != 0 {
		t.Errorf("p ran %+v without a permission hint", *calls)
	}
}

func TestLooksLikePermissionProblem(t *testing.T) {
	for msg, want := range map[string]bool{
		"recording failed: exit status 1\n[AVFoundation indev @ 0x1] Cannot open": true,
		"Capture screen 0 not available":                                          true,
		"operation not permitted":                                                 true,
		"encoding failed: no such file":                                           false,
		"recording failed: ffmpeg did not stop":                                   false,
	} {
		if got := looksLikePermissionProblem(errors.New(msg)); got != want {
			t.Errorf("%q: got %v, want %v", msg, got, want)
		}
	}
	if looksLikePermissionProblem(nil) {
		t.Error("nil error flagged")
	}
}

func TestOutputDir(t *testing.T) {
	dir := t.TempDir()
	m := New(allTools, Config{Encoder: encoder.DefaultOptions, OutputDir: dir})

	next, _ := m.startEncoding("in.mov")
	m = next.(Model)
	m.cancelEncode()

	if filepath.Dir(m.output) != dir || !strings.HasPrefix(filepath.Base(m.output), "prgif-") {
		t.Errorf("output = %q, want a timestamped GIF in %q", m.output, dir)
	}
}
