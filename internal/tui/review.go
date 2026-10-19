package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/monsteronfire/prgif/internal/encoder"
)

// estimateDebounce is how long settings must stay unchanged before a new
// size estimate starts, so holding an arrow key doesn't start one per step.
const estimateDebounce = 400 * time.Millisecond

type (
	// reviewMsg opens the Review screen for input. The --encode flag sends it
	// on startup; a finished recording goes there directly.
	reviewMsg struct{ input string }

	// estimateDueMsg fires when the debounce after a settings change ends.
	estimateDueMsg struct{ seq int }

	// estimateDoneMsg carries a finished (or failed) size estimate. full is
	// the probed length of the input, or 0 if it was already known.
	estimateDoneMsg struct {
		seq      int
		full     time.Duration
		estimate encoder.SizeEstimate
		err      error
	}
)

// tools returns the encoder's external programs, and whether both were found.
func (m Model) tools() (encoder.Tools, bool) {
	t := encoder.Tools{FFmpeg: m.deps.Path("ffmpeg"), Gifsicle: m.deps.Path("gifsicle")}
	return t, t.FFmpeg != "" && t.Gifsicle != ""
}

// startReview shows the Review screen for input and starts estimating.
func (m Model) startReview(input string) (tea.Model, tea.Cmd) {
	if _, ok := m.tools(); !ok {
		return m.fail(errors.New("ffmpeg and gifsicle are both needed to encode; see the install hints on the start screen"))
	}
	if input != m.input {
		m.input = input
		m.clipDuration = 0 // probed by the first estimate
		m.cleanupPreview()
	}
	m.state = stateReview
	m.notice = ""
	m.estimate, m.estimateErr = encoder.SizeEstimate{}, nil
	if !slices.Contains(m.visibleSettings(), m.setting) {
		m.setting = settingFPS
	}
	return m.startEstimate()
}

// scheduleEstimate restarts the debounce after a settings change. The
// running estimate is stale, so it's cancelled now rather than later.
func (m Model) scheduleEstimate() (tea.Model, tea.Cmd) {
	m.stopEstimate()
	m.estimateSeq++
	m.estimating = true
	seq := m.estimateSeq
	due := tea.Tick(estimateDebounce, func(time.Time) tea.Msg { return estimateDueMsg{seq: seq} })
	return m, tea.Batch(due, m.spinner.Tick)
}

// startEstimate starts estimating the GIF size for the current settings.
func (m Model) startEstimate() (tea.Model, tea.Cmd) {
	m.stopEstimate()
	m.estimateSeq++
	m.estimating = true
	ctx, cancel := context.WithCancel(context.Background())
	m.cancelEstimate = cancel

	seq, in, full, opts := m.estimateSeq, m.input, m.clipDuration, m.encodeOptions()
	tools, _ := m.tools()
	run := func() tea.Msg {
		probed := time.Duration(0)
		if full == 0 {
			var err error
			if full, err = encoder.ProbeDuration(ctx, tools.FFmpeg, in); err != nil {
				return estimateDoneMsg{seq: seq, err: err}
			}
			probed = full
		}
		est, err := encoder.Estimate(ctx, tools, in, full, opts)
		return estimateDoneMsg{seq: seq, full: probed, estimate: est, err: err}
	}
	return m, tea.Batch(run, m.spinner.Tick)
}

// stopEstimate cancels the running estimate, if any.
func (m *Model) stopEstimate() {
	if m.cancelEstimate != nil {
		m.cancelEstimate()
		m.cancelEstimate = nil
	}
}

func (m Model) handleEstimateDue(msg estimateDueMsg) (tea.Model, tea.Cmd) {
	if m.state != stateReview || msg.seq != m.estimateSeq {
		return m, nil // superseded by a later change, or Review was left
	}
	return m.startEstimate()
}

func (m Model) finishEstimate(msg estimateDoneMsg) (tea.Model, tea.Cmd) {
	// A cancelled or superseded estimate still delivers its message; only
	// the latest one describes the current settings.
	if m.state != stateReview || msg.seq != m.estimateSeq {
		return m, nil
	}
	m.stopEstimate() // release the context
	m.estimating = false
	if msg.full > 0 {
		m.clipDuration = msg.full
	}
	m.estimate, m.estimateErr = msg.estimate, msg.err
	return m, nil
}

func (m Model) handleReviewKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Record):
		m.stopEstimate()
		m.estimating = false
		return m.startEncoding(m.input)
	case key.Matches(msg, m.keys.Up):
		m.setting = m.nextSetting(-1)
	case key.Matches(msg, m.keys.Down):
		m.setting = m.nextSetting(+1)
	case key.Matches(msg, m.keys.Left), key.Matches(msg, m.keys.Right):
		dir := -1
		if key.Matches(msg, m.keys.Right) {
			dir = +1
		}
		opts := m.setting.adjust(m.cfg.Encoder, dir)
		if opts == m.cfg.Encoder {
			return m, nil // already at the end of the choices
		}
		m.cfg.Encoder = opts
		return m.scheduleEstimate()
	case key.Matches(msg, m.keys.Shuffle) && m.cfg.Encoder.Background.Enabled():
		m.cfg.Encoder.Background = m.cfg.Encoder.Background.Shuffle()
		return m.scheduleEstimate()
	case key.Matches(msg, m.keys.Preview):
		return m.startPreview()
	case key.Matches(msg, m.keys.Discard):
		return m.discardReview(), nil
	case key.Matches(msg, m.keys.Quit):
		m.stopEstimate()
		m.cleanupRecording()
		m.cleanupPreview()
		return m, tea.Quit
	}
	return m, nil
}

// discardReview leaves Review without encoding, deleting our recording (an
// --encode input is left alone).
func (m Model) discardReview() Model {
	m.stopEstimate()
	m.estimating = false
	m.notice = ""
	if m.recordDir != "" {
		m.notice = "Recording discarded"
	}
	m.cleanupRecording()
	m.cleanupPreview()
	m.input, m.clipDuration = "", 0
	m.state = stateIdle
	if !m.region.IsZero() {
		m.state = stateReady
	}
	return m
}

// visibleSettings are the settings shown on the current screen. An --encode
// input is never trimmed, so its Review screen leaves trim out.
func (m Model) visibleSettings() []setting {
	all := []setting{settingFPS, settingWidth, settingLossy, settingSpeed, settingTrim, settingBackground}
	if m.state == stateReview && m.recordPath == "" {
		return slices.DeleteFunc(all, func(s setting) bool { return s == settingTrim })
	}
	return all
}

// nextSetting returns the visible setting dir rows from the highlighted one,
// wrapping around.
func (m Model) nextSetting(dir int) setting {
	vis := m.visibleSettings()
	i := max(slices.Index(vis, m.setting), 0)
	return vis[(i+dir+len(vis))%len(vis)]
}

func (m Model) reviewView() string {
	opts := m.encodeOptions()
	var lines []string
	if !m.region.IsZero() {
		lines = append(lines, m.regionRow())
	}
	lines = append(lines,
		row("clip", m.clipText(opts)),
		row("gif", m.gifDimsText()),
		row("size", m.estimateText()),
		"",
	)
	for _, s := range m.visibleSettings() {
		lines = append(lines, m.settingRow(s))
	}
	lines = append(lines, "", hintStyle.Width(contentWidth).Render(m.setting.description()))
	if m.expectedBytes() > githubImageLimit {
		lines = append(lines, "",
			warnStyle.Width(contentWidth).Render(fmt.Sprintf(
				"⚠ Likely over GitHub's %s image limit. Try a lower fps or width, or a higher lossy level.",
				humanBytes(githubImageLimit))),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left, lines...)
}

// clipText shows how long the GIF will play, as on the Done screen.
func (m Model) clipText(opts encoder.Options) string {
	if m.clipDuration == 0 {
		return hintStyle.Render("measuring…")
	}
	kept := encoder.KeepDuration(m.clipDuration, opts.TrimEnd)
	return clipSummary(opts.OutputDuration(kept), speedOf(opts), m.clipDuration-kept)
}

// clipSummary renders "00:02.5 · 2× speed · 0.5s trimmed from the end",
// leaving out the parts that don't apply.
func clipSummary(playback time.Duration, speed float64, trimmed time.Duration) string {
	s := formatClock(playback)
	if speed != 1 {
		s += fmt.Sprintf(" · %s speed", formatSpeed(speed))
	}
	if trimmed > 0 {
		s += fmt.Sprintf(" · %s trimmed from the end", formatSeconds(trimmed))
	}
	return s
}

// gifDimsText predicts the GIF's dimensions from the region, or for an
// --encode input (no region) takes them from the latest estimate.
func (m Model) gifDimsText() string {
	if !m.region.IsZero() {
		w, h := outputSize(m.region, m.cfg.Encoder)
		return fmt.Sprintf("%d×%d", w, h)
	}
	if m.estimate.Width == 0 {
		return hintStyle.Render("measuring…")
	}
	return fmt.Sprintf("%d×%d", m.estimate.Width, m.estimate.Height)
}

// expectedBytes is the best known size of the GIF for the current settings:
// the preview's if it's up to date, else the finished estimate's, else 0.
func (m Model) expectedBytes() int64 {
	switch {
	case m.previewMatches():
		return m.preview.result.Size
	case m.estimating:
		return 0
	}
	return m.estimate.Bytes
}

// estimateText renders the size estimate, or the preview's exact size when
// it's up to date. While a new estimate is calculating, the previous figure
// stays visible but dimmed, so it doesn't flicker.
func (m Model) estimateText() string {
	e := m.estimate
	switch {
	case m.previewMatches():
		s := humanBytes(m.preview.result.Size) + " (preview)"
		if m.preview.result.Size > githubImageLimit {
			return warnStyle.Render(s)
		}
		return s
	case m.estimating && e.Bytes == 0:
		return m.spinner.View() + " " + hintStyle.Render("estimating…")
	case m.estimating:
		return m.spinner.View() + " " + hintStyle.Render(formatEstimate(e))
	case m.estimateErr != nil:
		return hintStyle.Render("estimate unavailable; you can still make the GIF")
	case e.Bytes > githubImageLimit:
		return warnStyle.Render(formatEstimate(e))
	}
	return formatEstimate(e)
}

// formatEstimate renders "≈ 3.4 MB (2.1–4.8 MB)", or just "3.4 MB" when the
// estimate is exact.
func formatEstimate(e encoder.SizeEstimate) string {
	if e.Exact {
		return humanBytes(e.Bytes)
	}
	return fmt.Sprintf("≈ %s (%s)", humanBytes(e.Bytes), humanRange(e.Low, e.High))
}

// humanRange renders "2.1–4.8 MB", or "800 KB–1.2 MB" across units.
func humanRange(lo, hi int64) string {
	l, h := humanBytes(lo), humanBytes(hi)
	if lu, hu := l[strings.LastIndex(l, " "):], h[strings.LastIndex(h, " "):]; lu == hu {
		l = strings.TrimSuffix(l, lu)
	}
	return l + "–" + h
}
