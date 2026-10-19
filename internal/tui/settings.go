package tui

import (
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/monsteronfire/prgif/internal/encoder"
	"github.com/monsteronfire/prgif/internal/selector"
)

// setting is one adjustable encoder option on the Ready screen.
type setting int

const (
	settingFPS setting = iota
	settingWidth
	settingLossy
	settingSpeed
	settingTrim
	settingBackground
	settingCount
)

var (
	// Recording happens at 30 fps, so there's no point going higher.
	fpsChoices   = []int{5, 8, 10, 12, 15, 20, 24, 30}
	widthChoices = []int{480, 640, 720, 800, 960, 1024, 1280, 1600}
	lossyChoices = []int{0, 20, 40, 60, 80, 100, 120, 150, 200}
	trimChoices  = []int{0, 250, 500, 750, 1000, 1500, 2000, 3000} // milliseconds
	speedChoices = []int{100, 125, 150, 200, 300, 400}             // percent
	// Background padding, as a percentage of the recording's width.
	backgroundChoices = []int{0, 4, 6, 8, 10, 12, 16}
)

func (s setting) label() string {
	switch s {
	case settingFPS:
		return "fps"
	case settingWidth:
		return "max width"
	case settingLossy:
		return "lossy"
	case settingSpeed:
		return "speed"
	case settingTrim:
		return "trim end"
	case settingBackground:
		return "background"
	}
	return ""
}

func (s setting) description() string {
	switch s {
	case settingFPS:
		return "Frames per second in the GIF. Lower is smaller; 10–15 suits UI demos."
	case settingWidth:
		return "Wider recordings are scaled down to this. Narrower ones keep their size."
	case settingLossy:
		return "gifsicle lossy compression. 0 is lossless; higher is smaller but grainier."
	case settingSpeed:
		return "Plays the recording faster. 2× halves the GIF's length, and usually its size."
	case settingTrim:
		return "Cut from the end of the recording, so switching back here to stop isn't in the GIF."
	case settingBackground:
		return "Frames the recording on a gradient, with rounded corners and a shadow."
	}
	return ""
}

func (s setting) choices() []int {
	switch s {
	case settingFPS:
		return fpsChoices
	case settingWidth:
		return widthChoices
	case settingLossy:
		return lossyChoices
	case settingSpeed:
		return speedChoices
	case settingTrim:
		return trimChoices
	case settingBackground:
		return backgroundChoices
	}
	return nil
}

// value returns the setting's current value in opts.
func (s setting) value(opts encoder.Options) int {
	switch s {
	case settingFPS:
		return opts.FPS
	case settingWidth:
		return opts.MaxWidth
	case settingLossy:
		return opts.Lossy
	case settingSpeed:
		return int(math.Round(speedOf(opts) * 100))
	case settingTrim:
		return int(opts.TrimEnd / time.Millisecond)
	case settingBackground:
		return opts.Background.Padding
	}
	return 0
}

// format renders a value for display.
func (s setting) format(v int) string {
	switch {
	case s == settingWidth:
		return fmt.Sprintf("%dpx", v)
	case (s == settingLossy || s == settingTrim || s == settingBackground) && v == 0:
		return "off"
	case s == settingTrim:
		return formatSeconds(time.Duration(v) * time.Millisecond)
	case s == settingSpeed:
		return formatSpeed(float64(v) / 100)
	case s == settingBackground:
		return fmt.Sprintf("%d%%", v)
	}
	return fmt.Sprint(v)
}

// adjust moves the setting one choice in direction dir (-1 or +1).
func (s setting) adjust(opts encoder.Options, dir int) encoder.Options {
	v := step(s.choices(), s.value(opts), dir)
	switch s {
	case settingFPS:
		opts.FPS = v
	case settingWidth:
		opts.MaxWidth = v
	case settingLossy:
		opts.Lossy = v
	case settingSpeed:
		opts.Speed = float64(v) / 100
	case settingTrim:
		opts.TrimEnd = time.Duration(v) * time.Millisecond
	case settingBackground:
		opts.Background.Padding = v
	}
	return opts
}

// speedOf returns opts.Speed, with 0 meaning normal speed.
func speedOf(opts encoder.Options) float64 {
	if opts.Speed == 0 {
		return 1
	}
	return opts.Speed
}

// formatSpeed renders a speed as "1×", "1.25×".
func formatSpeed(s float64) string {
	return strconv.FormatFloat(s, 'f', -1, 64) + "×"
}

// formatSeconds renders d as "0.5s", "1s", "1.25s".
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64) + "s"
}

// canAdjust reports whether adjust in direction dir would change the value.
func (s setting) canAdjust(opts encoder.Options, dir int) bool {
	v := s.value(opts)
	return step(s.choices(), v, dir) != v
}

// step returns the choice next to cur in direction dir, clamped to the ends.
// A cur that isn't one of the choices (e.g. from a command-line flag) moves to
// the nearest choice in that direction.
func step(choices []int, cur, dir int) int {
	if len(choices) == 0 {
		return cur
	}
	if i := slices.Index(choices, cur); i >= 0 {
		return choices[min(max(i+dir, 0), len(choices)-1)]
	}
	if dir > 0 {
		for _, c := range choices {
			if c > cur {
				return c
			}
		}
		return cur
	}
	for _, c := range slices.Backward(choices) {
		if c < cur {
			return c
		}
	}
	return cur
}

// outputSize predicts the GIF dimensions for region r, background included.
func outputSize(r selector.Region, opts encoder.Options) (w, h int) {
	l := opts.Layout(r.W, r.H)
	return l.Width, l.Height
}

// gradientSwatch previews the background's colours as a short bar.
func gradientSwatch(g encoder.Gradient) string {
	const cells = 8
	var b strings.Builder
	for i := range cells {
		b.WriteString(lipgloss.NewStyle().Foreground(g.At(float64(i) / (cells - 1))).Render("█"))
	}
	return b.String()
}
