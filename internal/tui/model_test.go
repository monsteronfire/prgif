package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/monsteronfire/prgif/internal/deps"
	"github.com/monsteronfire/prgif/internal/encoder"
)

var allTools = deps.Report{
	{Tool: deps.Required[0], Path: "/usr/bin/ffmpeg"},
	{Tool: deps.Required[1], Path: "/usr/bin/gifsicle"},
	{Tool: deps.Required[2], Path: "/usr/bin/select-region"},
}

func render(m Model) string {
	return ansi.Strip(m.View().Content)
}

// update applies msg and returns the concrete model.
func update(t *testing.T, m Model, msg tea.Msg) (Model, tea.Cmd) {
	t.Helper()
	next, cmd := m.Update(msg)
	return next.(Model), cmd
}

// encodingModel returns a model in the Encoding state without running the
// encoder, and a channel standing in for the encoder goroutine.
func encodingModel() (Model, chan tea.Msg, *bool) {
	ch := make(chan tea.Msg, 1)
	cancelled := false
	m := New(allTools, Config{Encoder: encoder.DefaultOptions})
	m.state = stateEncoding
	m.encodeCh = ch
	m.cancelEncode = func() { cancelled = true }
	m.encodeStart = time.Now()
	m.output = "out.gif"
	return m, ch, &cancelled
}

func isQuit(cmd tea.Cmd) bool {
	if cmd == nil {
		return false
	}
	_, ok := cmd().(tea.QuitMsg)
	return ok
}

func TestViewListsMissingTools(t *testing.T) {
	report := deps.Report{
		{Tool: deps.Required[0]},                            // ffmpeg missing
		{Tool: deps.Required[1], Path: "/usr/bin/gifsicle"}, // gifsicle found
	}

	out := render(New(report, Config{}))

	for _, want := range []string{"Missing tools", "ffmpeg", "brew install ffmpeg"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "brew install gifsicle") {
		t.Errorf("view lists gifsicle as missing although it was found:\n%s", out)
	}
}

func TestViewAllToolsFound(t *testing.T) {
	out := render(New(allTools, Config{}))

	if strings.Contains(out, "Missing tools") {
		t.Errorf("view reports missing tools:\n%s", out)
	}
	if !strings.Contains(out, "found") {
		t.Errorf("view lacks confirmation:\n%s", out)
	}
}

func TestInitStartsReviewWithEncodeFlag(t *testing.T) {
	if cmd := New(allTools, Config{}).Init(); cmd != nil {
		t.Error("Init without --encode returned a command")
	}
	cmd := New(allTools, Config{EncodeFile: "clip.mov"}).Init()
	if cmd == nil {
		t.Fatal("Init with --encode returned nil")
	}
	if msg, ok := cmd().(reviewMsg); !ok || msg.input != "clip.mov" {
		t.Errorf("Init sent %#v, want reviewMsg{clip.mov}", cmd())
	}
}

func TestStartEncodingWithoutToolsFails(t *testing.T) {
	m := New(deps.Report{{Tool: deps.Required[0]}}, Config{})

	m, _ = update(t, m, reviewMsg{input: "clip.mov"})

	if m.state != stateError {
		t.Fatalf("state = %v, want error", m.state)
	}
	if out := render(m); !strings.Contains(out, "ffmpeg and gifsicle") {
		t.Errorf("error view lacks reason:\n%s", out)
	}
}

func TestProgressUpdatesStage(t *testing.T) {
	m, _, _ := encodingModel()

	m, cmd := update(t, m, encodeProgressMsg{Stage: encoder.StageOptimise, Fraction: 0.9})

	if m.stage != encoder.StageOptimise {
		t.Errorf("stage = %v, want optimise", m.stage)
	}
	if cmd == nil {
		t.Error("no command returned; the model must keep listening to the encoder")
	}
	if out := render(m); !strings.Contains(out, "Optimising") {
		t.Errorf("view lacks stage:\n%s", out)
	}
}

func TestEncodeDone(t *testing.T) {
	m, _, _ := encodingModel()
	res := encoder.Result{Path: "out.gif", Size: 420_000, Width: 800, Height: 427, Frames: 36}

	m, _ = update(t, m, encodeDoneMsg{result: res})

	if m.state != stateDone {
		t.Fatalf("state = %v, want done", m.state)
	}
	out := render(m)
	for _, want := range []string{"GIF ready", "out.gif", "420.0 KB", "800×427", "36 frames", "12 fps"} {
		if !strings.Contains(out, want) {
			t.Errorf("done view missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "GitHub") {
		t.Errorf("size warning shown for a small GIF:\n%s", out)
	}
}

func TestEncodeDoneOverGitHubLimit(t *testing.T) {
	m, _, _ := encodingModel()

	m, _ = update(t, m, encodeDoneMsg{result: encoder.Result{Size: githubImageLimit + 1, Frames: 1}})

	if out := render(m); !strings.Contains(out, "Over GitHub's 10.0 MB image limit") {
		t.Errorf("no size warning:\n%s", out)
	}
}

func TestEncodeFailed(t *testing.T) {
	m, _, _ := encodingModel()

	m, _ = update(t, m, encodeDoneMsg{err: errors.New("ffmpeg: exit status 1")})

	if m.state != stateError {
		t.Fatalf("state = %v, want error", m.state)
	}
	if out := render(m); !strings.Contains(out, "encoding failed: ffmpeg: exit status 1") {
		t.Errorf("error view lacks message:\n%s", out)
	}
}

func TestQuitWhileEncodingCancelsThenQuits(t *testing.T) {
	m, _, cancelled := encodingModel()

	m, cmd := update(t, m, tea.KeyPressMsg{Code: 'q', Text: "q"})

	if !*cancelled {
		t.Error("encoder not cancelled")
	}
	if m.state != stateCancelling {
		t.Errorf("state = %v, want cancelling", m.state)
	}
	if isQuit(cmd) {
		t.Error("quit before the encoder stopped")
	}
	if out := render(m); !strings.Contains(out, "Cancelling") {
		t.Errorf("view lacks cancelling status:\n%s", out)
	}

	_, cmd = update(t, m, encodeDoneMsg{err: context.Canceled})
	if !isQuit(cmd) {
		t.Error("did not quit after the encoder stopped")
	}
}

func TestHumanBytes(t *testing.T) {
	tests := map[int64]string{
		512:        "512 B",
		420_000:    "420.0 KB",
		9_600_000:  "9.6 MB",
		10_000_000: "10.0 MB",
	}
	for n, want := range tests {
		if got := humanBytes(n); got != want {
			t.Errorf("humanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestDisplayPath(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"out.gif":                         "out.gif",
		filepath.Join(wd, "sub", "x.gif"): filepath.Join("sub", "x.gif"),
		"/definitely/elsewhere/x.gif":     "/definitely/elsewhere/x.gif",
	}
	if !strings.HasPrefix(wd, home) {
		tests[filepath.Join(home, "Desktop", "x.gif")] = "~/Desktop/x.gif"
	}
	for in, want := range tests {
		if got := displayPath(in); got != want {
			t.Errorf("displayPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDoneViewWrapsLongPaths(t *testing.T) {
	m, _, _ := encodingModel()
	long := "/definitely/elsewhere/" + strings.Repeat("very-long-directory-name/", 4) + "out.gif"

	m, _ = update(t, m, encodeDoneMsg{result: encoder.Result{Path: long, Frames: 1}})

	out := render(m)
	if !strings.Contains(out, "out.gif") {
		t.Errorf("end of path not visible:\n%s", out)
	}
	for line := range strings.SplitSeq(out, "\n") {
		if w := ansi.StringWidth(line); w > contentWidth+8 { // + border and padding
			t.Errorf("line %d wide:\n%s", w, line)
		}
	}
}

func TestDefaultOutputName(t *testing.T) {
	got := defaultOutputName(time.Date(2026, 10, 7, 17, 4, 5, 0, time.UTC))
	if want := "prgif-20261007-170405.gif"; got != want {
		t.Errorf("defaultOutputName = %q, want %q", got, want)
	}
}
