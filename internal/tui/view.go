package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// View implements tea.Model.
func (m Model) View() tea.View {
	var body string
	switch m.state {
	case stateIdle:
		body = m.idleView()
	case stateSelecting:
		body = m.selectingView()
	case stateReady:
		body = m.readyView()
	case stateCountdown:
		body = m.countdownView()
	case stateRecording:
		body = m.recordingView()
	case stateStopping:
		body = m.spinner.View() + " " + bodyStyle.Render("Finishing recording…")
	case stateReview:
		body = m.reviewView()
	case statePreviewing:
		body = m.previewingView()
	case stateEncoding, stateCancelling:
		body = m.encodingView()
	case stateDone:
		body = m.doneView()
	case stateError:
		body = m.errorView()
	}

	parts := []string{
		titleStyle.Render("prgif"),
		subtitleStyle.Render("screen area → optimised GIF for pull requests"),
		"",
		body,
	}
	if m.notice != "" {
		parts = append(parts, "", hintStyle.Width(contentWidth).Render(m.notice))
	}
	if h := m.help.View(m.helpBindings()); h != "" {
		parts = append(parts, "", h)
	}

	content := panelStyle.Render(lipgloss.JoinVertical(lipgloss.Left, parts...))
	if m.width > 0 && m.height > 0 {
		content = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
	}

	v := tea.NewView(content)
	v.AltScreen = true
	return v
}

func (m Model) idleView() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		bodyStyle.Render("Select an area of your screen, record it,\nand export a GIF small enough for GitHub."),
		"",
		m.depsView(),
	)
}

// depsView lists missing tools with install hints, or confirms all are present.
func (m Model) depsView() string {
	missing := m.deps.Missing()
	if len(missing) == 0 {
		return okStyle.Render("✓ ffmpeg, gifsicle and select-region found")
	}

	var b strings.Builder
	b.WriteString(warnStyle.Render("⚠ Missing tools"))
	for _, s := range missing {
		b.WriteString("\n  ")
		b.WriteString(toolNameStyle.Render(s.Name))
		b.WriteString(hintStyle.Render(" — " + s.Purpose))
		b.WriteString("\n    ")
		b.WriteString(codeStyle.Render(s.Install))
	}
	return b.String()
}

func (m Model) selectingView() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		m.spinner.View()+" "+bodyStyle.Render("Drag on screen to select an area…"),
		"",
		hintStyle.Render("Esc or right-click on the overlay to cancel."),
	)
}

func (m Model) readyView() string {
	r := m.region
	gifW, gifH := outputSize(r, m.cfg.Encoder)

	lines := []string{
		m.regionRow(),
		row("gif", fmt.Sprintf("%d×%d", gifW, gifH)),
		"",
	}
	for s := range settingCount {
		lines = append(lines, m.settingRow(s))
	}
	lines = append(lines, "", hintStyle.Width(contentWidth).Render(m.setting.description()))
	if !m.keys.Record.Enabled() {
		lines = append(lines, "", warnStyle.Render("⚠ Install ffmpeg and gifsicle to record."))
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m Model) regionRow() string {
	r := m.region
	return row("region", fmt.Sprintf("%s · screen %d @%gx", r, r.ScreenIndex, r.Scale))
}

func (m Model) countdownView() string {
	return lipgloss.JoinVertical(lipgloss.Left,
		bodyStyle.Render("Recording starts in ")+countdownStyle.Render(fmt.Sprint(m.countdown)),
		"",
		m.regionRow(),
		"",
		hintStyle.Width(contentWidth).Render(
			"This terminal stays on screen while recording, so keep it outside the selected area. Come back here to stop."),
	)
}

func (m Model) recordingView() string {
	elapsed := time.Since(m.recorder.Started)
	dot := "●"
	if elapsed/(500*time.Millisecond)%2 == 1 {
		dot = " " // blink
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		recStyle.Render(dot+" REC ")+bodyStyle.Render(formatClock(elapsed)),
		"",
		m.regionRow(),
	)
}

// settingRow renders one adjustable setting, with arrows showing which
// directions it can still move.
func (m Model) settingRow(s setting) string {
	arrow := func(dir int, glyph string) string {
		if s != m.setting || !s.canAdjust(m.cfg.Encoder, dir) {
			return " "
		}
		return codeStyle.Render(glyph)
	}

	cursor, label := "  ", settingLabelStyle.Render(s.label())
	if s == m.setting {
		cursor, label = codeStyle.Render("› "), settingLabelActiveStyle.Render(s.label())
	}
	value := settingValueStyle.Render(s.format(s.value(m.cfg.Encoder)))
	line := cursor + label + arrow(-1, "◂") + " " + value + " " + arrow(+1, "▸")
	if bg := m.cfg.Encoder.Background; s == settingBackground && bg.Enabled() {
		line += " " + gradientSwatch(bg.Gradient())
		if s == m.setting {
			line += hintStyle.Render(" g shuffle")
		}
	}
	return line
}

func (m Model) encodingView() string {
	status := m.stage.String() + "…"
	if m.state == stateCancelling {
		status = "Cancelling…"
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.spinner.View()+" "+bodyStyle.Render(status),
		"",
		m.progress.View(),
		"",
		row("output", displayPath(m.output)),
	)
}

func (m Model) doneView() string {
	r := m.result
	lines := []string{
		headingOKStyle.Render("✓ GIF ready"),
		"",
		row("path", displayPath(r.Path)),
		row("size", humanBytes(r.Size)),
		row("dims", fmt.Sprintf("%d×%d · %d frames · %d fps", r.Width, r.Height, r.Frames, m.cfg.Encoder.FPS)),
	}
	if r.Duration > 0 {
		lines = append(lines, row("clip", clipSummary(r.Duration, speedOf(m.cfg.Encoder), r.Trimmed)))
	}
	lines = append(lines, row("took", m.elapsed.Round(100*time.Millisecond).String()))
	if r.Size > githubImageLimit {
		lines = append(lines, "",
			warnStyle.Width(contentWidth).Render(fmt.Sprintf(
				"⚠ Over GitHub's %s image limit. Press t to re-encode with a lower fps or width, or a higher lossy level.",
				humanBytes(githubImageLimit))),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

func (m Model) errorView() string {
	lines := []string{
		errorStyle.Render("✗ Something went wrong"),
		"",
		bodyStyle.Width(contentWidth).Render(m.err.Error()),
	}
	if m.permissionHint {
		lines = append(lines, "",
			warnStyle.Width(contentWidth).Render(
				"This is often macOS blocking screen capture. Allow your terminal app under "+
					"System Settings → Privacy & Security → Screen Recording, then restart the terminal."),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// row renders a "label  value" line, wrapping long values beside the label.
func row(label, value string) string {
	valueWidth := contentWidth - labelStyle.GetWidth()
	return lipgloss.JoinHorizontal(lipgloss.Top,
		labelStyle.Render(label),
		bodyStyle.Width(valueWidth).Render(value),
	)
}

// displayPath shortens p for display: relative to the working directory when
// inside it, otherwise with the home directory as ~.
func displayPath(p string) string {
	abs, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, abs); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(abs, home+string(filepath.Separator)) {
		return "~" + strings.TrimPrefix(abs, home)
	}
	return abs
}
