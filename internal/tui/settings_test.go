package tui

import (
	"testing"
	"time"

	"github.com/monsteronfire/prgif/internal/encoder"
	"github.com/monsteronfire/prgif/internal/selector"
)

func TestStep(t *testing.T) {
	choices := []int{5, 10, 15}
	tests := []struct {
		cur, dir, want int
	}{
		{10, +1, 15},
		{10, -1, 5},
		{15, +1, 15}, // clamped at the top
		{5, -1, 5},   // clamped at the bottom
		{12, +1, 15}, // off-list values move to the nearest choice
		{12, -1, 10},
		{20, +1, 20}, // nothing higher: unchanged
		{20, -1, 15},
		{1, -1, 1},
	}
	for _, tt := range tests {
		if got := step(choices, tt.cur, tt.dir); got != tt.want {
			t.Errorf("step(%d, %+d) = %d, want %d", tt.cur, tt.dir, got, tt.want)
		}
	}
}

func TestAdjustChangesOnlyItsSetting(t *testing.T) {
	opts := encoder.Options{FPS: 12, MaxWidth: 800, Lossy: 80, TrimEnd: 500 * time.Millisecond}

	if got := settingFPS.adjust(opts, +1); got != (encoder.Options{FPS: 15, MaxWidth: 800, Lossy: 80, TrimEnd: 500 * time.Millisecond}) {
		t.Errorf("fps+ = %+v", got)
	}
	if got := settingWidth.adjust(opts, -1); got != (encoder.Options{FPS: 12, MaxWidth: 720, Lossy: 80, TrimEnd: 500 * time.Millisecond}) {
		t.Errorf("width- = %+v", got)
	}
	if got := settingLossy.adjust(opts, +1); got != (encoder.Options{FPS: 12, MaxWidth: 800, Lossy: 100, TrimEnd: 500 * time.Millisecond}) {
		t.Errorf("lossy+ = %+v", got)
	}
	if got := settingTrim.adjust(opts, +1); got != (encoder.Options{FPS: 12, MaxWidth: 800, Lossy: 80, TrimEnd: 750 * time.Millisecond}) {
		t.Errorf("trim+ = %+v", got)
	}
	if got := settingTrim.adjust(encoder.Options{TrimEnd: 250 * time.Millisecond}, -1); got.TrimEnd != 0 {
		t.Errorf("trim- from 0.25s = %v, want 0", got.TrimEnd)
	}
}

func TestEveryChoiceIsValid(t *testing.T) {
	for s := range settingCount {
		for _, v := range s.choices() {
			opts := encoder.DefaultOptions
			switch s {
			case settingFPS:
				opts.FPS = v
			case settingWidth:
				opts.MaxWidth = v
			case settingLossy:
				opts.Lossy = v
			case settingTrim:
				opts.TrimEnd = time.Duration(v) * time.Millisecond
			case settingSpeed:
				opts.Speed = float64(v) / 100
			case settingBackground:
				opts.Background.Padding = v
			}
			if err := opts.Validate(); err != nil {
				t.Errorf("%s=%d: %v", s.label(), v, err)
			}
		}
	}
}

func TestDefaultsAreChoices(t *testing.T) {
	for s := range settingCount {
		v := s.value(encoder.DefaultOptions)
		if step(s.choices(), v, +1) == v && step(s.choices(), v, -1) == v {
			t.Errorf("default %s=%d is stuck", s.label(), v)
		}
	}
}

func TestSpeedSetting(t *testing.T) {
	opts := encoder.DefaultOptions
	for _, want := range []float64{1.25, 1.5, 2, 3, 4, 4} { // clamped at 4×
		opts = settingSpeed.adjust(opts, +1)
		if opts.Speed != want {
			t.Errorf("speed = %g, want %g", opts.Speed, want)
		}
	}
	opts.Speed = 0 // unset means 1×
	if v := settingSpeed.value(opts); v != 100 {
		t.Errorf("value of unset speed = %d, want 100", v)
	}
	for v, want := range map[int]string{100: "1×", 125: "1.25×", 200: "2×"} {
		if got := settingSpeed.format(v); got != want {
			t.Errorf("format(%d) = %q, want %q", v, got, want)
		}
	}
}

func TestFormatTrim(t *testing.T) {
	for ms, want := range map[int]string{0: "off", 250: "0.25s", 500: "0.5s", 1000: "1s", 1500: "1.5s"} {
		if got := settingTrim.format(ms); got != want {
			t.Errorf("format(%d) = %q, want %q", ms, got, want)
		}
	}
}

func TestOutputSize(t *testing.T) {
	tests := []struct {
		r            selector.Region
		maxW         int
		wantW, wantH int
	}{
		{selector.Region{W: 1200, H: 640}, 800, 800, 427},
		{selector.Region{W: 400, H: 300}, 800, 400, 300},
		{selector.Region{W: 800, H: 600}, 800, 800, 600},
	}
	for _, tt := range tests {
		w, h := outputSize(tt.r, encoder.Options{MaxWidth: tt.maxW})
		if w != tt.wantW || h != tt.wantH {
			t.Errorf("outputSize(%dx%d, %d) = %dx%d, want %dx%d", tt.r.W, tt.r.H, tt.maxW, w, h, tt.wantW, tt.wantH)
		}
	}
}

func TestOutputSizeWithBackground(t *testing.T) {
	opts := encoder.Options{MaxWidth: 800, Background: encoder.Background{Padding: 10}}
	if w, h := outputSize(selector.Region{W: 400, H: 300}, opts); w != 480 || h != 380 {
		t.Errorf("outputSize = %dx%d, want 480x380", w, h)
	}
	if w, _ := outputSize(selector.Region{W: 1200, H: 640}, opts); w != 800 {
		t.Errorf("outputSize width = %d, want capped at 800", w)
	}
}

func TestFormatBackground(t *testing.T) {
	for v, want := range map[int]string{0: "off", 8: "8%"} {
		if got := settingBackground.format(v); got != want {
			t.Errorf("format(%d) = %q, want %q", v, got, want)
		}
	}
}
