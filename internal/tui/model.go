// Package tui implements the prgif terminal interface.
package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/progress"
	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"

	"github.com/monsteronfire/prgif/internal/deps"
	"github.com/monsteronfire/prgif/internal/encoder"
	"github.com/monsteronfire/prgif/internal/recorder"
	"github.com/monsteronfire/prgif/internal/selector"
)

// state is a step in the select → record → encode flow.
type state int

const (
	stateIdle       state = iota
	stateSelecting        // overlay helper running
	stateReady            // region chosen; settings adjustable
	stateCountdown        // 3-2-1 before recording
	stateRecording        // ffmpeg capturing
	stateStopping         // waiting for ffmpeg to finalise the recording
	stateReview           // recording finished; settings adjustable, size estimated
	statePreviewing       // encoding a preview from Review
	stateEncoding         // encoder running
	stateCancelling       // quit pressed while encoding; waiting for it to stop
	stateDone             // GIF written
	stateError            // something failed; err says what
)

// Config holds settings from the command line.
type Config struct {
	Encoder encoder.Options
	// Output is the GIF path. Empty means a timestamped name in OutputDir.
	Output string
	// OutputDir is where timestamped GIFs are saved. Empty means the
	// current directory. Ignored when Output is set.
	OutputDir string
	// EncodeFile, when set, skips selection and recording and encodes this
	// existing video on startup.
	EncodeFile string
}

// Model is the root Bubble Tea model.
type Model struct {
	state state
	cfg   Config
	deps  deps.Report
	keys  keyMap
	help  help.Model

	// notice is a one-line status shown on the Idle and Ready screens,
	// e.g. "Selection cancelled".
	notice string

	// Selection.
	region       selector.Region
	cancelSelect context.CancelFunc
	setting      setting // highlighted row on the Ready screen

	// Recording. frame dims the screen around the region during the
	// countdown and recording, if the helper can draw one.
	frame      *selector.Frame
	countdown  int
	recorder   *recorder.Recorder
	recordPath string // the recording, inside recordDir
	// recordDir is the temp dir holding the recording. It's kept after
	// encoding so the GIF can be re-encoded, and removed on discard, record
	// again, or quit.
	recordDir string

	// Review. input is the video to encode: our recording or an --encode file.
	input          string
	clipDuration   time.Duration // input's full length; 0 until probed
	estimate       encoder.SizeEstimate
	estimateErr    error
	estimating     bool
	estimateSeq    int // identifies the latest estimate; older results are dropped
	cancelEstimate context.CancelFunc

	// Preview. preview is the latest finished one; pendingPreview is being
	// encoded.
	preview          previewGIF
	pendingPreview   previewGIF
	previewCancelled bool

	// Encoding.
	encodeCh     <-chan tea.Msg
	cancelEncode context.CancelFunc
	encodeStart  time.Time
	stage        encoder.Stage
	spinner      spinner.Model
	progress     progress.Model

	// Results.
	output   string
	result   encoder.Result
	elapsed  time.Duration
	gifsMade int // GIFs written this session
	err      error
	// permissionHint marks a recording error that looks like missing
	// Screen Recording permission.
	permissionHint bool

	width  int
	height int
}

// regionSelectedMsg carries the overlay helper's result.
type regionSelectedMsg struct {
	region selector.Region
	err    error
}

// New returns a model. report is the result of deps.Check.
func New(report deps.Report, cfg Config) Model {
	keys := newKeyMap()
	keys.Select.SetEnabled(report.Path("select-region") != "")
	keys.Record.SetEnabled(report.Path("ffmpeg") != "" && report.Path("gifsicle") != "")

	return Model{
		state: stateIdle,
		cfg:   cfg,
		deps:  report,
		keys:  keys,
		help:  help.New(),
		spinner: spinner.New(
			spinner.WithSpinner(spinner.Dot),
			spinner.WithStyle(spinnerStyle),
		),
		progress: progress.New(
			progress.WithColors(colorAccent, colorWarn),
			progress.WithWidth(contentWidth-10),
		),
	}
}

// Init implements tea.Model.
func (m Model) Init() tea.Cmd {
	if m.cfg.EncodeFile != "" {
		return func() tea.Msg { return reviewMsg{input: m.cfg.EncodeFile} }
	}
	return nil
}

// Update implements tea.Model.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.help.SetWidth(msg.Width)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case regionSelectedMsg:
		return m.finishSelecting(msg)

	case countdownTickMsg:
		return m.handleCountdownTick()

	case recordTickMsg:
		if m.state != stateRecording {
			return m, nil
		}
		return m, recordTick() // redraw the clock

	case recorderExitedMsg:
		return m.handleRecorderExited(msg)

	case recordingStoppedMsg:
		return m.finishRecording(msg)

	case reviewMsg:
		return m.startReview(msg.input)

	case estimateDueMsg:
		return m.handleEstimateDue(msg)

	case estimateDoneMsg:
		return m.finishEstimate(msg)

	case encodeProgressMsg:
		if m.state != stateEncoding && m.state != statePreviewing {
			return m, waitForEncode(m.encodeCh)
		}
		m.stage = msg.Stage
		return m, tea.Batch(m.progress.SetPercent(msg.Fraction), waitForEncode(m.encodeCh))

	case encodeDoneMsg:
		return m.finishEncoding(msg)

	case actionDoneMsg:
		if msg.err != nil {
			m.notice = fmt.Sprintf("%s: %v", msg.failure, msg.err)
		} else {
			m.notice = msg.success
		}
		return m, nil

	case spinner.TickMsg:
		switch {
		case m.state == stateSelecting, m.state == stateStopping, m.state == stateEncoding, m.state == stateCancelling,
			m.state == statePreviewing, m.state == stateReview && m.estimating:
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			return m, cmd
		}
		return m, nil // let the spinner stop

	case progress.FrameMsg:
		var cmd tea.Cmd
		m.progress, cmd = m.progress.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m Model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch m.state {
	case stateSelecting:
		if key.Matches(msg, m.keys.Cancel) {
			m.cancelSelect() // closes the overlay; regionSelectedMsg follows
		}
		return m, nil

	case stateReady:
		switch {
		case key.Matches(msg, m.keys.Record):
			return m.startCountdown()
		case key.Matches(msg, m.keys.Up):
			m.setting = m.nextSetting(-1)
		case key.Matches(msg, m.keys.Down):
			m.setting = m.nextSetting(+1)
		case key.Matches(msg, m.keys.Left):
			m.cfg.Encoder = m.setting.adjust(m.cfg.Encoder, -1)
		case key.Matches(msg, m.keys.Right):
			m.cfg.Encoder = m.setting.adjust(m.cfg.Encoder, +1)
		case key.Matches(msg, m.keys.Shuffle) && m.cfg.Encoder.Background.Enabled():
			m.cfg.Encoder.Background = m.cfg.Encoder.Background.Shuffle()
		case key.Matches(msg, m.keys.Select):
			return m.startSelecting()
		case key.Matches(msg, m.keys.Quit):
			return m, tea.Quit
		}
		return m, nil

	case stateCountdown:
		switch {
		case msg.String() == "ctrl+c":
			m.hideFrame()
			return m, tea.Quit
		case key.Matches(msg, m.keys.Discard):
			m.hideFrame()
			m.state = stateReady // the pending countdownTickMsg is ignored
		}
		return m, nil

	case stateRecording:
		// q deliberately does nothing here: it's too easy to lose a take.
		switch {
		case key.Matches(msg, m.keys.Stop):
			return m.stopRecording()
		case key.Matches(msg, m.keys.Discard):
			return m.discardRecording(), nil
		case msg.String() == "ctrl+c":
			return m.discardRecording(), tea.Quit
		}
		return m, nil

	case stateStopping:
		return m, nil // Stop gives up on ffmpeg within seconds

	case stateReview:
		return m.handleReviewKey(msg)

	case statePreviewing:
		if key.Matches(msg, m.keys.Cancel) && !m.previewCancelled {
			return m.cancelPreview()
		}
		return m, nil

	case stateEncoding:
		if key.Matches(msg, m.keys.Cancel) {
			// Stop ffmpeg/gifsicle before exiting so nothing is left running
			// or half-written; quit once the encodeDoneMsg arrives.
			m.state = stateCancelling
			m.cancelEncode()
		}
		return m, nil

	case stateCancelling:
		return m, nil // already on the way out

	case stateDone:
		switch {
		case key.Matches(msg, m.keys.Reveal):
			return m, revealInFinder(m.result.Path)
		case key.Matches(msg, m.keys.Copy):
			return m, copyPath(m.result.Path)
		case key.Matches(msg, m.keys.Tweak):
			return m.startReview(m.input)
		case key.Matches(msg, m.keys.Again):
			return m.recordAgain()
		}

	case stateError:
		switch {
		case key.Matches(msg, m.keys.Select):
			return m.startSelecting()
		case m.permissionHint && key.Matches(msg, m.keys.Settings):
			return m, openScreenRecordingSettings()
		}

	case stateIdle:
		if key.Matches(msg, m.keys.Select) {
			return m.startSelecting()
		}
	}

	if key.Matches(msg, m.keys.Quit) {
		m.cleanupRecording() // kept after encoding for re-encodes
		m.cleanupPreview()
		return m, tea.Quit
	}
	return m, nil
}

func (m Model) startSelecting() (tea.Model, tea.Cmd) {
	helper := m.deps.Path("select-region")
	if helper == "" {
		return m.fail(errors.New("the select-region helper is missing; build it on the Mac with make helper"))
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.cancelSelect = cancel
	m.state = stateSelecting
	m.notice = ""
	m.err = nil
	run := func() tea.Msg {
		r, err := selector.Run(ctx, helper)
		return regionSelectedMsg{region: r, err: err}
	}
	return m, tea.Batch(run, m.spinner.Tick)
}

func (m Model) finishSelecting(msg regionSelectedMsg) (tea.Model, tea.Cmd) {
	m.cancelSelect() // release the context

	switch {
	case errors.Is(msg.err, selector.ErrCancelled), errors.Is(msg.err, context.Canceled):
		// Back to where we were, keeping any earlier selection.
		m.notice = "Selection cancelled"
		m.state = stateIdle
		if !m.region.IsZero() {
			m.state = stateReady
		}
		return m, nil
	case msg.err != nil:
		return m.fail(fmt.Errorf("selecting an area: %w", msg.err))
	}

	m.region = msg.region
	m.state = stateReady
	return m, nil
}

func (m Model) startEncoding(input string) (tea.Model, tea.Cmd) {
	tools, ok := m.tools()
	if !ok {
		return m.fail(errors.New("ffmpeg and gifsicle are both needed to encode; see the install hints on the start screen"))
	}
	m.input = input
	if m.previewMatches() {
		return m.savePreview()
	}
	m.cleanupPreview() // out of date

	m.output = m.nextOutput()
	m.notice = ""

	opts := m.encodeOptions()

	ctx, cancel := context.WithCancel(context.Background())
	m.state = stateEncoding
	m.cancelEncode = cancel
	m.encodeStart = time.Now()
	m.stage = encoder.StagePalette
	m.encodeCh = runEncode(ctx, tools, input, m.output, opts)
	return m, tea.Batch(waitForEncode(m.encodeCh), m.spinner.Tick, m.progress.SetPercent(0))
}

// nextOutput returns the path for the next GIF.
func (m Model) nextOutput() string {
	switch {
	case m.cfg.Output == "":
		return filepath.Join(m.cfg.OutputDir, defaultOutputName(time.Now()))
	case m.gifsMade > 0:
		return availablePath(m.cfg.Output) // don't overwrite this session's earlier GIF
	default:
		return m.cfg.Output
	}
}

// encodeOptions returns the encoder settings for the current input. Only our
// own recordings are trimmed; an --encode input is used as is.
func (m Model) encodeOptions() encoder.Options {
	opts := m.cfg.Encoder
	if m.recordPath == "" {
		opts.TrimEnd = 0
	}
	return opts
}

func (m Model) finishEncoding(msg encodeDoneMsg) (tea.Model, tea.Cmd) {
	if m.state == statePreviewing {
		return m.finishPreview(msg)
	}
	m.cancelEncode() // release the context
	m.encodeCh = nil

	if m.state == stateCancelling {
		m.cleanupRecording()
		return m, tea.Quit
	}
	if msg.err != nil {
		err := fmt.Errorf("encoding failed: %w", msg.err)
		if m.recordDir != "" {
			// Keep the take so it can be re-encoded rather than re-recorded.
			err = fmt.Errorf("%w\n\nThe recording was kept; retry with:\nprgif --encode %s", err, m.recordPath)
			m.recordDir = ""
		}
		return m.fail(err)
	}
	m.gifsMade++
	m.state = stateDone
	m.result = msg.result
	m.elapsed = time.Since(m.encodeStart)
	return m, nil
}

func (m Model) fail(err error) (tea.Model, tea.Cmd) {
	m.hideFrame()
	m.state = stateError
	m.err = err
	m.notice = ""
	m.permissionHint = false
	return m, nil
}

// failRecording is fail for recording errors, adding the Screen Recording
// permission hint when the error looks like one.
func (m Model) failRecording(err error) (tea.Model, tea.Cmd) {
	next, cmd := m.fail(err)
	mm := next.(Model)
	mm.permissionHint = looksLikePermissionProblem(err)
	return mm, cmd
}

// recordAgain goes back to Ready with the same region and settings, or to
// selecting when there is no region yet (after --encode).
func (m Model) recordAgain() (tea.Model, tea.Cmd) {
	m.cleanupRecording()
	m.cleanupPreview()
	m.input, m.clipDuration = "", 0
	m.notice = ""
	if !m.region.IsZero() {
		m.state = stateReady
		return m, nil
	}
	if m.keys.Select.Enabled() {
		return m.startSelecting()
	}
	m.state = stateIdle
	return m, nil
}
