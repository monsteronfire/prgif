package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/monsteronfire/prgif/internal/deps"
	"github.com/monsteronfire/prgif/internal/encoder"
	"github.com/monsteronfire/prgif/internal/selector"
)

var (
	keyS     = tea.KeyPressMsg{Code: 's', Text: "s"}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	keyDown  = tea.KeyPressMsg{Code: tea.KeyDown}
	keyUp    = tea.KeyPressMsg{Code: tea.KeyUp}
	keyRight = tea.KeyPressMsg{Code: tea.KeyRight}
	keyLeft  = tea.KeyPressMsg{Code: tea.KeyLeft}
	keyG     = tea.KeyPressMsg{Code: 'g', Text: "g"}
)

// modelWithHelper returns a model whose select-region is a shell script.
func modelWithHelper(t *testing.T, script string) Model {
	t.Helper()
	helper := filepath.Join(t.TempDir(), "select-region")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := deps.Report{
		{Tool: deps.Required[0], Path: "/usr/bin/ffmpeg"},
		{Tool: deps.Required[1], Path: "/usr/bin/gifsicle"},
		{Tool: deps.Required[2], Path: helper},
	}
	return New(report, Config{Encoder: encoder.DefaultOptions})
}

// runUntil executes cmd (expanding batches) and returns the first message of
// type T. Other messages, like spinner ticks, are dropped.
func runUntil[T tea.Msg](t *testing.T, cmd tea.Cmd) T {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		switch msg := c().(type) {
		case tea.BatchMsg:
			queue = append(queue, msg...)
		case T:
			return msg
		}
	}
	var zero T
	t.Fatalf("no %T produced", zero)
	return zero
}

// selectWith presses s, runs the helper and feeds its result back in.
func selectWith(t *testing.T, m Model) Model {
	t.Helper()
	m, cmd := update(t, m, keyS)
	if m.state != stateSelecting {
		t.Fatalf("state after s = %v, want selecting", m.state)
	}
	if !strings.Contains(render(m), "Drag on screen") {
		t.Errorf("selecting view lacks instructions:\n%s", render(m))
	}
	m, _ = update(t, m, runUntil[regionSelectedMsg](t, cmd))
	return m
}

const retinaSelection = `echo '{"displayID":1,"h":320,"scale":2,"screenIndex":0,"w":600,"x":412,"y":188}'`

func TestSelectRegion(t *testing.T) {
	m := selectWith(t, modelWithHelper(t, retinaSelection))

	if m.state != stateReady {
		t.Fatalf("state = %v, want ready", m.state)
	}
	if want := (selector.Region{X: 824, Y: 376, W: 1200, H: 640, DisplayID: 1, Scale: 2}); m.region != want {
		t.Errorf("region = %+v, want %+v", m.region, want)
	}
	out := render(m)
	for _, want := range []string{"1200×640 at 824,376", "screen 0 @2x", "800×427", "fps", "12", "800px", "lossy", "80", "reselect"} {
		if !strings.Contains(out, want) {
			t.Errorf("ready view missing %q:\n%s", want, out)
		}
	}
}

func TestReadyAdjustsSettings(t *testing.T) {
	m := selectWith(t, modelWithHelper(t, retinaSelection))

	m, _ = update(t, m, keyDown)  // → max width
	m, _ = update(t, m, keyRight) // 800 → 960
	if m.setting != settingWidth || m.cfg.Encoder.MaxWidth != 960 {
		t.Fatalf("setting=%v width=%d, want width 960", m.setting, m.cfg.Encoder.MaxWidth)
	}
	if out := render(m); !strings.Contains(out, "960×512") || !strings.Contains(out, "scaled down") {
		t.Errorf("view not updated for width 960:\n%s", out)
	}

	m, _ = update(t, m, keyUp)   // → fps
	m, _ = update(t, m, keyUp)   // wraps → background
	m, _ = update(t, m, keyUp)   // → trim end
	m, _ = update(t, m, keyUp)   // → speed
	m, _ = update(t, m, keyUp)   // → lossy
	m, _ = update(t, m, keyLeft) // 80 → 60
	if m.setting != settingLossy || m.cfg.Encoder.Lossy != 60 {
		t.Errorf("setting=%v lossy=%d, want lossy 60", m.setting, m.cfg.Encoder.Lossy)
	}
	m, _ = update(t, m, keyDown)  // → speed
	m, _ = update(t, m, keyRight) // 1× → 1.25×
	if out := render(m); m.setting != settingSpeed || m.cfg.Encoder.Speed != 1.25 || !strings.Contains(out, "1.25×") {
		t.Errorf("speed setting not adjusted:\n%s", out)
	}
	m, _ = update(t, m, keyDown)  // → trim end
	m, _ = update(t, m, keyRight) // 0.5s → 0.75s
	if out := render(m); m.setting != settingTrim || !strings.Contains(out, "0.75s") || !strings.Contains(out, "isn't in the GIF") {
		t.Errorf("trim setting not adjusted:\n%s", out)
	}
	if m.cfg.Encoder.FPS != 12 {
		t.Errorf("fps changed to %d", m.cfg.Encoder.FPS)
	}

	// g does nothing until the background is on.
	if m, _ = update(t, m, keyG); m.cfg.Encoder.Background.Seed != encoder.DefaultOptions.Background.Seed {
		t.Error("g shuffled a background that's off")
	}
	m, _ = update(t, m, keyDown)  // → background
	m, _ = update(t, m, keyRight) // off → 4%
	if out := render(m); m.cfg.Encoder.Background.Padding != 4 || !strings.Contains(out, "4%") || !strings.Contains(out, "g shuffle") {
		t.Errorf("background setting not adjusted:\n%s", out)
	}
	// The recording shrinks to make room for the padding within the max width.
	if out := render(m); !strings.Contains(out, "960×546") {
		t.Errorf("gif size doesn't stay within max width:\n%s", out)
	}
	seed := m.cfg.Encoder.Background.Seed
	if m, _ = update(t, m, keyG); m.cfg.Encoder.Background.Seed == seed {
		t.Error("g didn't shuffle the colours")
	}
}

func TestSelectCancelledFromIdle(t *testing.T) {
	m := selectWith(t, modelWithHelper(t, `exit 1`))

	if m.state != stateIdle {
		t.Errorf("state = %v, want idle", m.state)
	}
	if out := render(m); !strings.Contains(out, "Selection cancelled") {
		t.Errorf("no cancelled notice:\n%s", out)
	}
}

func TestReselectCancelledKeepsRegion(t *testing.T) {
	m := selectWith(t, modelWithHelper(t, retinaSelection))
	first := m.region

	// Swap in a helper that cancels, then reselect.
	cancelling := modelWithHelper(t, `exit 1`)
	m.deps = cancelling.deps
	m = selectWith(t, m)

	if m.state != stateReady || m.region != first {
		t.Errorf("state=%v region=%+v, want ready with %+v", m.state, m.region, first)
	}
}

func TestSelectHelperFails(t *testing.T) {
	m := selectWith(t, modelWithHelper(t, `echo "select-region: no displays found" >&2; exit 2`))

	if m.state != stateError {
		t.Fatalf("state = %v, want error", m.state)
	}
	out := render(m)
	if !strings.Contains(out, "no displays found") || !strings.Contains(out, "try again") {
		t.Errorf("error view lacks reason or retry hint:\n%s", out)
	}

	// s retries from the error screen.
	if m, _ = update(t, m, keyS); m.state != stateSelecting {
		t.Errorf("state after retry = %v, want selecting", m.state)
	}
}

func TestCancelKeyWhileSelecting(t *testing.T) {
	m := modelWithHelper(t, retinaSelection)
	m, _ = update(t, m, keyS)
	cancelled := false
	m.cancelSelect = func() { cancelled = true }

	update(t, m, keyEsc)

	if !cancelled {
		t.Error("Esc did not cancel the overlay")
	}
}

func TestSelectDisabledWithoutHelper(t *testing.T) {
	m := New(deps.Report{{Tool: deps.Required[2]}}, Config{Encoder: encoder.DefaultOptions})

	if strings.Contains(render(m), "select area") {
		t.Errorf("help offers select without the helper:\n%s", render(m))
	}
	if m, _ = update(t, m, keyS); m.state != stateIdle {
		t.Errorf("s changed state to %v without the helper", m.state)
	}
}
