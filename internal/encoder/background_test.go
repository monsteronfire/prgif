package encoder

import (
	"context"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

func TestLayoutFitsMaxWidth(t *testing.T) {
	opts := Options{MaxWidth: 800, Background: Background{Padding: 8}}
	l := opts.Layout(1200, 640)
	// The whole GIF fills MaxWidth exactly; the recording shrinks to make room.
	if l.Width != 800 || l.ContentW+2*l.Pad != l.Width || l.ContentH+2*l.Pad != l.Height {
		t.Errorf("layout %+v doesn't add up to 800 wide", l)
	}
	if l.Pad != 55 || l.ContentW != 690 || l.ContentH != 368 {
		t.Errorf("layout %+v, want pad 55 around 690x368", l)
	}
}

func TestLayoutNeverUpscales(t *testing.T) {
	opts := Options{MaxWidth: 800, Background: Background{Padding: 10}}
	l := opts.Layout(400, 300)
	if l.ContentW != 400 || l.ContentH != 300 || l.Pad != 40 || l.Width != 480 || l.Height != 380 {
		t.Errorf("layout %+v, want 400x300 padded by 40", l)
	}
}

func TestLayoutWithoutBackground(t *testing.T) {
	l := Options{MaxWidth: 800}.Layout(1200, 640)
	if l.Width != 800 || l.Height != 427 || l.Pad != 0 {
		t.Errorf("layout %+v, want plain 800x427", l)
	}
}

func TestGradientFollowsSeed(t *testing.T) {
	a, b := Background{Seed: 7}.Gradient(), Background{Seed: 7}.Gradient()
	if a != b {
		t.Errorf("same seed gave %+v and %+v", a, b)
	}
	if c := (Background{Seed: 8}).Gradient(); c == a {
		t.Errorf("seeds 7 and 8 gave the same gradient %+v", a)
	}
}

func TestHSLRoundTrip(t *testing.T) {
	for _, h := range []float64{0, 45, 120, 200, 300} {
		c := hsl(h, 0.7, 0.6)
		h2, s2, l2 := toHSL(c)
		if back := hsl(h2, s2, l2); back != c {
			t.Errorf("hue %v: %v round-tripped to %v", h, c, back)
		}
	}
}

func TestRenderBackdrop(t *testing.T) {
	l := Options{MaxWidth: 800, Background: Background{Padding: 10}}.Layout(400, 300)
	img := renderBackdrop(l, Background{Seed: 1}.Gradient())
	alpha := func(x, y int) uint8 { return img.NRGBAAt(x, y).A }

	if a := alpha(0, 0); a != 255 {
		t.Errorf("background corner alpha = %d, want opaque", a)
	}
	if a := alpha(l.Width/2, l.Height/2); a != 0 {
		t.Errorf("centre alpha = %d, want a hole for the recording", a)
	}
	// Just inside the recording's straight edge: still the recording.
	if a := alpha(l.Pad, l.Height/2); a != 0 {
		t.Errorf("edge alpha = %d, want the recording", a)
	}
	// The recording's own corner pixel is rounded off.
	if a := alpha(l.Pad, l.Pad); a == 0 {
		t.Error("corner of the recording isn't rounded")
	}
}

func TestParseSize(t *testing.T) {
	banner := `Input #0, mov,mp4,m4a,3gp,3g2,mj2, from 'rec.mov':
  Duration: 00:00:04.03, start: 0.000000, bitrate: 52045 kb/s
  Stream #0:0[0x1](und): Video: h264 (High 4:4:4 Predictive) (avc1 / 0x31637661), yuv444p(pc, progressive), 2400x1280, 52040 kb/s, 30 fps, 30 tbr, 15360 tbn (default)`
	w, h, ok := ParseSize(banner)
	if !ok || w != 2400 || h != 1280 {
		t.Errorf("ParseSize = %d, %d, %v; want 2400, 1280", w, h, ok)
	}
	if _, _, ok := ParseSize("no streams here"); ok {
		t.Error("ParseSize found a size in nonsense")
	}
}

func TestValidateBackground(t *testing.T) {
	for _, p := range []int{-1, MaxPadding + 1} {
		o := DefaultOptions
		o.Background.Padding = p
		if o.Validate() == nil {
			t.Errorf("padding %d accepted", p)
		}
	}
}

func TestArgsWithBackdrop(t *testing.T) {
	bg := &backdrop{path: "bg.png", layout: Layout{Width: 800, Height: 478, ContentW: 690, ContentH: 368, Pad: 55}}
	opts := DefaultOptions

	pal := PaletteArgs("in.mov", "pal.png", Span{}, opts, bg)
	assertFlag(t, pal, "-lavfi", "fps=12,scale=690:368:flags=lanczos,pad=800:478:55:55[v];[v][1:v]overlay=format=rgb,format=rgb24,palettegen=stats_mode=full:max_colors=256")

	frames := FramesArgs("in.mov", "pal.png", "raw.gif", Span{}, opts, bg)
	assertFlag(t, frames, "-lavfi", "fps=12,scale=690:368:flags=lanczos,pad=800:478:55:55[v];[v][1:v]overlay=format=rgb,format=rgb24[x];[x][2:v]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle")
	// Inputs: video, background (1:v), palette (2:v).
	if v, b, p := slices.Index(frames, "in.mov"), slices.Index(frames, "bg.png"), slices.Index(frames, "pal.png"); v > b || b > p {
		t.Errorf("inputs out of order: %v", frames)
	}
}

func TestEncodeWithBackground(t *testing.T) {
	tools := requireTools(t)
	clip := makeSourceClip(t, tools, "testsrc", 8)
	ctx := context.Background()

	opts := DefaultOptions
	opts.Background = Background{Padding: 8, Seed: 42}
	res, err := Encode(ctx, tools, clip, filepath.Join(t.TempDir(), "bg.gif"), opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	if l := opts.Layout(1200, 640); res.Width != l.Width || res.Height != l.Height {
		t.Errorf("size = %dx%d, want %dx%d", res.Width, res.Height, l.Width, l.Height)
	}

	est, err := Estimate(ctx, tools, clip, 8*time.Second, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("estimate %d (%d–%d); actual %d", est.Bytes, est.Low, est.High, res.Size)
	if est.Width != res.Width || est.Height != res.Height || res.Size < est.Low || res.Size > est.High {
		t.Errorf("estimate %dx%d %d–%d, actual %dx%d %d", est.Width, est.Height, est.Low, est.High, res.Width, res.Height, res.Size)
	}
}
