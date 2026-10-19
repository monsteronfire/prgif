package tui

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/monsteronfire/prgif/internal/encoder"
)

// previewGIF is a GIF encoded from Review to look at before saving. If the
// settings haven't changed since, saving moves it into place rather than
// encoding again.
type previewGIF struct {
	dir    string // temp dir holding the GIF; "" when there's no preview
	input  string
	opts   encoder.Options
	result encoder.Result // Path is inside dir
	took   time.Duration
}

// startPreview encodes the GIF with the current settings to a temp file and
// opens it in Quick Look. An up-to-date preview is just reopened.
func (m Model) startPreview() (tea.Model, tea.Cmd) {
	if m.previewMatches() {
		return m, openPreview(m.preview.result.Path)
	}
	tools, ok := m.tools()
	if !ok {
		return m.fail(fmt.Errorf("ffmpeg and gifsicle are both needed to encode; see the install hints on the start screen"))
	}
	m.cleanupPreview()
	dir, err := os.MkdirTemp("", "prgif-preview-")
	if err != nil {
		m.notice = fmt.Sprintf("Couldn't make a preview: %v", err)
		return m, nil
	}

	// The preview's own size replaces the estimate, and both would compete
	// for the CPU.
	m.stopEstimate()
	m.estimating = false

	opts := m.encodeOptions()
	ctx, cancel := context.WithCancel(context.Background())
	m.state = statePreviewing
	m.notice = ""
	m.pendingPreview = previewGIF{dir: dir, input: m.input, opts: opts}
	m.cancelEncode = cancel
	m.encodeStart = time.Now()
	m.stage = encoder.StagePalette
	m.encodeCh = runEncode(ctx, tools, m.input, filepath.Join(dir, "preview.gif"), opts)
	return m, tea.Batch(waitForEncode(m.encodeCh), m.spinner.Tick, m.progress.SetPercent(0))
}

// cancelPreview stops the preview encode; finishPreview returns to Review
// once the encoder has stopped.
func (m Model) cancelPreview() (tea.Model, tea.Cmd) {
	m.previewCancelled = true
	m.cancelEncode()
	return m, nil
}

func (m Model) finishPreview(msg encodeDoneMsg) (tea.Model, tea.Cmd) {
	m.cancelEncode() // release the context
	m.encodeCh = nil
	p := m.pendingPreview
	m.pendingPreview = previewGIF{}
	cancelled := m.previewCancelled
	m.previewCancelled = false
	m.state = stateReview

	if cancelled || msg.err != nil {
		_ = os.RemoveAll(p.dir)
		if !cancelled {
			m.notice = "Preview failed: " + firstLine(msg.err.Error())
		}
		return m.startEstimate() // it was stopped for the preview
	}
	p.result = msg.result
	p.took = time.Since(m.encodeStart)
	m.preview = p
	m.notice = "Preview opened in Quick Look. Press enter to save it."
	return m, openPreview(p.result.Path)
}

// previewMatches reports whether the preview was made from the current input
// and settings, so it's exactly the GIF that saving would produce.
func (m Model) previewMatches() bool {
	return m.preview.dir != "" && m.preview.input == m.input && m.preview.opts == m.encodeOptions()
}

// savePreview moves the up-to-date preview to the output path instead of
// encoding again.
func (m Model) savePreview() (tea.Model, tea.Cmd) {
	m.stopEstimate()
	m.estimating = false
	m.output = m.nextOutput()
	if err := moveFile(m.preview.result.Path, m.output); err != nil {
		return m.fail(fmt.Errorf("saving the preview: %w", err))
	}
	res := m.preview.result
	res.Path = m.output
	m.elapsed = m.preview.took
	m.cleanupPreview()
	m.notice = ""
	m.gifsMade++
	m.state = stateDone
	m.result = res
	return m, nil
}

// cleanupPreview deletes the preview, if any.
func (m *Model) cleanupPreview() {
	if m.preview.dir != "" {
		_ = os.RemoveAll(m.preview.dir)
	}
	m.preview = previewGIF{}
}

// openPreview shows path in Quick Look, which plays GIFs. qlmanage returns
// when the Quick Look window is closed.
func openPreview(path string) tea.Cmd {
	return runAction("Preview closed. Press enter to save it, or change settings and press p again.",
		"Couldn't open Quick Look", "", "qlmanage", "-p", absPath(path))
}

// moveFile moves src to dst, copying when they're on different volumes (the
// temp dir and the Desktop can be).
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() { _ = in.Close() }() // read-only
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst) // don't leave a truncated GIF behind
		return err
	}
	if err := out.Close(); err != nil {
		_ = os.Remove(dst)
		return err
	}
	return os.Remove(src)
}

// firstLine returns s up to its first newline, e.g. an error without the
// ffmpeg output folded into it.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

func (m Model) previewingView() string {
	status := "Making preview: " + strings.ToLower(m.stage.String()) + "…"
	if m.previewCancelled {
		status = "Cancelling preview…"
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		m.spinner.View()+" "+bodyStyle.Render(status),
		"",
		m.progress.View(),
		"",
		hintStyle.Width(contentWidth).Render("The preview opens in Quick Look when it's ready. Nothing is saved until you press enter on the review screen."),
	)
}
