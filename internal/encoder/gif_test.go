package encoder

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestScaleFilterNeverUpscales(t *testing.T) {
	got := scaleFilter(Options{FPS: 12, MaxWidth: 800}, nil)
	want := "fps=12,scale='min(800,iw)':-1:flags=lanczos"
	if got != want {
		t.Errorf("scaleFilter = %q, want %q", got, want)
	}
}

func TestScaleFilterSpeedsUp(t *testing.T) {
	for speed, want := range map[float64]string{
		0:    "fps=12,scale='min(800,iw)':-1:flags=lanczos", // unset: normal speed
		1:    "fps=12,scale='min(800,iw)':-1:flags=lanczos",
		1.25: "setpts=PTS/1.25,fps=12,scale='min(800,iw)':-1:flags=lanczos",
		2:    "setpts=PTS/2,fps=12,scale='min(800,iw)':-1:flags=lanczos",
	} {
		if got := scaleFilter(Options{FPS: 12, MaxWidth: 800, Speed: speed}, nil); got != want {
			t.Errorf("speed %g: scaleFilter = %q, want %q", speed, got, want)
		}
	}
}

func TestOutputDuration(t *testing.T) {
	for speed, want := range map[float64]time.Duration{0: 6 * time.Second, 1: 6 * time.Second, 1.5: 4 * time.Second, 4: 1500 * time.Millisecond} {
		if got := (Options{Speed: speed}).OutputDuration(6 * time.Second); got != want {
			t.Errorf("speed %g: OutputDuration(6s) = %v, want %v", speed, got, want)
		}
	}
}

func TestValidateSpeed(t *testing.T) {
	for _, speed := range []float64{0.5, -1, MaxSpeed + 0.5} {
		o := DefaultOptions
		o.Speed = speed
		if o.Validate() == nil {
			t.Errorf("Speed %g accepted", speed)
		}
	}
	for _, speed := range []float64{0, 1, 2.5, MaxSpeed} {
		o := DefaultOptions
		o.Speed = speed
		if err := o.Validate(); err != nil {
			t.Errorf("Speed %g rejected: %v", speed, err)
		}
	}
}

func TestPaletteArgs(t *testing.T) {
	args := PaletteArgs("in.mov", "pal.png", Span{}, DefaultOptions, nil)

	assertFlag(t, args, "-i", "in.mov")
	assertFlag(t, args, "-lavfi", "fps=12,scale='min(800,iw)':-1:flags=lanczos,palettegen=stats_mode=diff:max_colors=256")
	if args[len(args)-1] != "pal.png" {
		t.Errorf("output = %q, want pal.png last", args[len(args)-1])
	}
}

func TestFramesArgs(t *testing.T) {
	args := FramesArgs("in.mov", "pal.png", "raw.gif", Span{}, DefaultOptions, nil)

	assertFlag(t, args, "-progress", "pipe:1")
	assertFlag(t, args, "-lavfi", "fps=12,scale='min(800,iw)':-1:flags=lanczos[x];[x][1:v]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle")
	assertFlag(t, args, "-loop", "0")
	// Inputs must be in order: video first, palette second (referenced as 1:v).
	first := slices.Index(args, "in.mov")
	second := slices.Index(args, "pal.png")
	if first < 0 || second < 0 || first > second {
		t.Errorf("inputs out of order: %v", args)
	}
	if args[len(args)-1] != "raw.gif" {
		t.Errorf("output = %q, want raw.gif last", args[len(args)-1])
	}
}

func TestArgsLimitInputLength(t *testing.T) {
	for name, args := range map[string][]string{
		"palette": PaletteArgs("in.mov", "pal.png", Span{Length: 2500 * time.Millisecond}, DefaultOptions, nil),
		"frames":  FramesArgs("in.mov", "pal.png", "raw.gif", Span{Length: 2500 * time.Millisecond}, DefaultOptions, nil),
	} {
		// -t must come before -i to limit the input rather than the output.
		i := slices.Index(args, "-t")
		if i < 0 || args[i+1] != "2.500" || args[i+2] != "-i" || args[i+3] != "in.mov" {
			t.Errorf("%s: -t 2.500 not right before -i in.mov: %v", name, args)
		}
	}
	args := PaletteArgs("in.mov", "pal.png", Span{}, DefaultOptions, nil)
	if slices.Contains(args, "-t") || slices.Contains(args, "-ss") {
		t.Errorf("-t or -ss added for an empty span: %v", args)
	}
}

func TestArgsSeekInput(t *testing.T) {
	args := FramesArgs("in.mov", "pal.png", "raw.gif", Span{Start: 4200 * time.Millisecond, Length: time.Second}, DefaultOptions, nil)

	// -ss and -t must both come before -i to seek the input.
	i := slices.Index(args, "-ss")
	want := []string{"-ss", "4.200", "-t", "1.000", "-i", "in.mov"}
	if i < 0 || !slices.Equal(args[i:i+len(want)], want) {
		t.Errorf("want %v in %v", want, args)
	}
}

func TestKeepDuration(t *testing.T) {
	tests := []struct {
		full, trim, want time.Duration
	}{
		{10 * time.Second, 500 * time.Millisecond, 9500 * time.Millisecond},
		{10 * time.Second, 0, 10 * time.Second},
		{800 * time.Millisecond, 500 * time.Millisecond, 500 * time.Millisecond}, // keeps minKeep
		{300 * time.Millisecond, 500 * time.Millisecond, 300 * time.Millisecond}, // shorter than minKeep: untouched
		{2 * time.Second, 2 * time.Second, 500 * time.Millisecond},
	}
	for _, tt := range tests {
		if got := KeepDuration(tt.full, tt.trim); got != tt.want {
			t.Errorf("KeepDuration(%v, %v) = %v, want %v", tt.full, tt.trim, got, tt.want)
		}
	}
}

func TestEstimateOptimise(t *testing.T) {
	raw := filepath.Join(t.TempDir(), "raw.gif")
	if err := os.WriteFile(raw, make([]byte, 500_000), 0o644); err != nil { // ~0.3s expected
		t.Fatal(err)
	}
	var mu sync.Mutex
	var got []float64
	stop := estimateOptimise(raw, func(f float64) {
		mu.Lock()
		got = append(got, f)
		mu.Unlock()
	})
	time.Sleep(time.Second) // well past the estimate
	stop()

	mu.Lock()
	n := len(got)
	reports := slices.Clone(got)
	mu.Unlock()
	if n < 3 {
		t.Fatalf("only %d reports", n)
	}
	for i, f := range reports {
		if f < 0 || f > 0.95 {
			t.Errorf("report %d = %v, want within [0, 0.95]", i, f)
		}
		if i > 0 && f < reports[i-1] {
			t.Errorf("estimate went backwards: %v", reports)
			break
		}
	}
	if last := reports[n-1]; last < 0.9 {
		t.Errorf("estimate only reached %v after 3× the expected time", last)
	}

	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if len(got) != n {
		t.Error("reports continued after stop")
	}
}

func TestValidateTrim(t *testing.T) {
	for _, trim := range []time.Duration{-time.Second, MaxTrimEnd + time.Millisecond} {
		o := DefaultOptions
		o.TrimEnd = trim
		if o.Validate() == nil {
			t.Errorf("TrimEnd %v accepted", trim)
		}
	}
}

func TestGifsicleArgs(t *testing.T) {
	tests := []struct {
		lossy int
		want  []string
	}{
		{80, []string{"-O2", "--lossy=80", "--colors", "256", "raw.gif", "-o", "out.gif"}},
		{0, []string{"-O2", "--colors", "256", "raw.gif", "-o", "out.gif"}},
	}
	for _, tt := range tests {
		got := GifsicleArgs("raw.gif", "out.gif", Options{Lossy: tt.lossy})
		if !slices.Equal(got, tt.want) {
			t.Errorf("lossy=%d: got %v, want %v", tt.lossy, got, tt.want)
		}
	}
}

func TestValidate(t *testing.T) {
	if err := DefaultOptions.Validate(); err != nil {
		t.Errorf("DefaultOptions invalid: %v", err)
	}
	for _, o := range []Options{
		{FPS: 0, MaxWidth: 800, Lossy: 80},
		{FPS: 12, MaxWidth: 0, Lossy: 80},
		{FPS: 12, MaxWidth: 800, Lossy: 201},
	} {
		if o.Validate() == nil {
			t.Errorf("Validate(%+v) = nil, want error", o)
		}
	}
}

func TestParseDuration(t *testing.T) {
	stderr := "Input #0, mov,mp4, from 'in.mov':\n  Duration: 00:01:03.50, start: 0.000000, bitrate: 1234 kb/s\n"
	got, ok := ParseDuration(stderr)
	if want := 63*time.Second + 500*time.Millisecond; !ok || got != want {
		t.Errorf("ParseDuration = %v, %v; want %v, true", got, ok, want)
	}
	if _, ok := ParseDuration("Duration: N/A"); ok {
		t.Error("ParseDuration(N/A) ok = true, want false")
	}
}

func TestParseProgress(t *testing.T) {
	out := strings.Join([]string{
		"frame=10", "out_time_us=1000000", "progress=continue",
		"frame=20", "out_time_us=3000000", "progress=continue",
		"out_time_us=N/A",
		"out_time_us=5000000", // past the end: clamped
		"progress=end",
	}, "\n")

	var got []float64
	if err := ParseProgress(strings.NewReader(out), 4*time.Second, func(f float64) { got = append(got, f) }); err != nil {
		t.Fatal(err)
	}

	want := []float64{0.25, 0.75, 1, 1}
	if !slices.Equal(got, want) {
		t.Errorf("fractions = %v, want %v", got, want)
	}
}

func TestParseProgressWithoutDuration(t *testing.T) {
	var got []float64
	if err := ParseProgress(strings.NewReader("out_time_us=1000000\nprogress=end\n"), 0, func(f float64) { got = append(got, f) }); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, []float64{1}) {
		t.Errorf("fractions = %v, want [1]", got)
	}
}

func TestParseInfo(t *testing.T) {
	tests := []struct {
		name, out    string
		frames, w, h int
		wantErr      bool
	}{
		{"many", "* out.gif 36 images\n  logical screen 800x427\n  global color table [256]\n", 36, 800, 427, false},
		{"one", "* one.gif 1 image\n  logical screen 320x200\n", 1, 320, 200, false},
		{"path with spaces", "* /tmp/my demo.gif 2 images\n  logical screen 10x10\n", 2, 10, 10, false},
		{"garbage", "nope", 0, 0, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ParseInfo(tt.out)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if r.Frames != tt.frames || r.Width != tt.w || r.Height != tt.h {
				t.Errorf("got %d frames %dx%d, want %d frames %dx%d", r.Frames, r.Width, r.Height, tt.frames, tt.w, tt.h)
			}
		})
	}
}

func assertFlag(t *testing.T, args []string, flag, value string) {
	t.Helper()
	i := slices.Index(args, flag)
	if i < 0 || i+1 >= len(args) {
		t.Errorf("flag %s missing from %v", flag, args)
		return
	}
	if args[i+1] != value {
		t.Errorf("%s = %q, want %q", flag, args[i+1], value)
	}
}
