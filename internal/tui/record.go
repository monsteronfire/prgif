package tui

import (
	"fmt"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/monsteronfire/prgif/internal/recorder"
	"github.com/monsteronfire/prgif/internal/selector"
)

// countdownFrom is how many seconds the countdown before recording lasts.
const countdownFrom = 3

type (
	countdownTickMsg struct{}
	recordTickMsg    struct{}

	// recorderExitedMsg is sent when ffmpeg exits, for any reason.
	recorderExitedMsg struct{ rec *recorder.Recorder }

	// recordingStoppedMsg is sent once a requested stop has finished.
	recordingStoppedMsg struct {
		rec *recorder.Recorder
		err error
	}
)

func countdownTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return countdownTickMsg{} })
}

func recordTick() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(time.Time) tea.Msg { return recordTickMsg{} })
}

// watchRecorder reports when ffmpeg exits so a failure (no Screen Recording
// permission, a crop outside the screen) surfaces immediately.
func watchRecorder(rec *recorder.Recorder) tea.Cmd {
	return func() tea.Msg {
		<-rec.Done()
		return recorderExitedMsg{rec: rec}
	}
}

func (m Model) startCountdown() (tea.Model, tea.Cmd) {
	m.state = stateCountdown
	m.countdown = countdownFrom
	m.notice = ""
	m.showFrame()
	return m, countdownTick()
}

func (m Model) handleCountdownTick() (tea.Model, tea.Cmd) {
	if m.state != stateCountdown {
		return m, nil // cancelled
	}
	m.countdown--
	if m.countdown > 0 {
		return m, countdownTick()
	}
	return m.startRecording()
}

func (m Model) startRecording() (tea.Model, tea.Cmd) {
	path, dir, err := recorder.TempPath()
	if err != nil {
		return m.fail(fmt.Errorf("creating a temporary file for the recording: %w", err))
	}
	rec, err := recorder.Start(m.deps.Path("ffmpeg"), m.region, path)
	if err != nil {
		_ = os.RemoveAll(dir)
		return m.failRecording(err)
	}
	m.state = stateRecording
	m.recorder = rec
	m.recordPath = path
	m.recordDir = dir
	return m, tea.Batch(recordTick(), watchRecorder(rec))
}

func (m Model) stopRecording() (tea.Model, tea.Cmd) {
	m.hideFrame()
	rec := m.recorder
	m.state = stateStopping
	stop := func() tea.Msg { return recordingStoppedMsg{rec: rec, err: rec.Stop()} }
	return m, tea.Batch(stop, m.spinner.Tick)
}

func (m Model) finishRecording(msg recordingStoppedMsg) (tea.Model, tea.Cmd) {
	if msg.rec != m.recorder {
		return m, nil
	}
	m.recorder = nil
	if msg.err != nil {
		m.cleanupRecording()
		return m.failRecording(msg.err)
	}
	return m.startReview(m.recordPath)
}

// handleRecorderExited fails the recording if ffmpeg exited without being
// asked to; after a requested stop, recordingStoppedMsg handles the result.
func (m Model) handleRecorderExited(msg recorderExitedMsg) (tea.Model, tea.Cmd) {
	if m.state != stateRecording || msg.rec != m.recorder {
		return m, nil
	}
	err := msg.rec.Err()
	m.recorder = nil
	m.cleanupRecording()
	return m.failRecording(err)
}

// discardRecording stops ffmpeg, deletes the recording and returns to Ready.
func (m Model) discardRecording() Model {
	m.hideFrame()
	if m.recorder != nil {
		m.recorder.Discard()
		m.recorder = nil
	}
	m.cleanupRecording()
	m.state = stateReady
	m.notice = "Recording discarded"
	return m
}

// showFrame dims the screen around the region and borders it, so the
// pointer can be kept inside it. It's only a guide: if the helper is too old to draw one, or
// fails, recording goes ahead without it.
func (m *Model) showFrame() {
	helper := m.deps.Path("select-region")
	if m.frame != nil || !m.region.CanFrame || helper == "" {
		return
	}
	m.frame, _ = selector.ShowFrame(helper, m.region)
}

// hideFrame removes the dimming and border, if shown.
func (m *Model) hideFrame() {
	if m.frame != nil {
		m.frame.Close()
		m.frame = nil
	}
}

// cleanupRecording deletes the temporary recording directory, if any. An
// --encode input is never deleted because it has no recordDir.
func (m *Model) cleanupRecording() {
	if m.recordDir != "" {
		_ = os.RemoveAll(m.recordDir)
		m.recordDir, m.recordPath = "", ""
	}
}

// formatClock renders d as "01:07.3".
func formatClock(d time.Duration) string {
	d = d.Round(100 * time.Millisecond)
	mins := int(d / time.Minute)
	secs := (d % time.Minute).Seconds()
	return fmt.Sprintf("%02d:%04.1f", mins, secs)
}
