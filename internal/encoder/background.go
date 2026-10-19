package encoder

import (
	"bufio"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
)

// Background frames the recording on a gradient, with padding, rounded
// corners and a soft shadow, like the backgrounds in Cap or CleanShot.
type Background struct {
	// Padding is the gap around the recording, as a percentage of its width,
	// from 0 (no background) to MaxPadding.
	Padding int
	// Seed picks the gradient. The same seed always gives the same colours.
	Seed uint64
}

// MaxPadding is the largest allowed Background.Padding.
const MaxPadding = 20

// Enabled reports whether the recording is framed at all.
func (b Background) Enabled() bool { return b.Padding > 0 }

// Shuffle returns b with a new random gradient.
func (b Background) Shuffle() Background {
	b.Seed = rand.Uint64()
	return b
}

// Gradient is a linear gradient between two colours.
type Gradient struct {
	From, To color.RGBA
	// Angle is the direction from From to To in degrees: 0 points right,
	// 90 points down.
	Angle float64
}

// Gradient returns the colours Seed picks. Hues are 30–100° apart and
// blended around the colour wheel, so the middle stays vivid rather than
// turning muddy, without becoming a rainbow.
func (b Background) Gradient() Gradient {
	r := rand.New(rand.NewPCG(b.Seed, b.Seed^0x9e3779b97f4a7c15))
	h1 := r.Float64() * 360
	h2 := h1 + (30+r.Float64()*70)*float64(1-2*r.IntN(2))
	s := 0.65 + r.Float64()*0.25
	return Gradient{
		From:  hsl(h1, s, 0.52+r.Float64()*0.12),
		To:    hsl(h2, s, 0.58+r.Float64()*0.14),
		Angle: r.Float64() * 360,
	}
}

// At returns the gradient's colour at fraction t from From to To.
func (g Gradient) At(t float64) color.RGBA {
	t = t * t * (3 - 2*t) // smoothstep, so neither end looks flat
	h1, s1, l1 := toHSL(g.From)
	h2, s2, l2 := toHSL(g.To)
	// Go the short way round the colour wheel.
	if d := h2 - h1; d > 180 {
		h2 -= 360
	} else if d < -180 {
		h2 += 360
	}
	return hsl(h1+(h2-h1)*t, s1+(s2-s1)*t, l1+(l2-l1)*t)
}

// Layout is where everything goes in a framed GIF, in output pixels.
type Layout struct {
	Width, Height        int // the whole GIF
	ContentW, ContentH   int // the scaled recording
	Pad                  int // gap on every side of the recording
	Radius               int // corner radius of the recording
	ShadowBlur, ShadowDY int
}

// Layout fits a recording of iw×ih and its background into MaxWidth, never
// upscaling the recording.
func (o Options) Layout(iw, ih int) Layout {
	f := float64(o.Background.Padding) / 100
	cw, pad := iw, int(math.Round(float64(iw)*f))
	if cw+2*pad > o.MaxWidth {
		// Too wide: fill MaxWidth exactly.
		pad = int(math.Round(float64(o.MaxWidth) * f / (1 + 2*f)))
		cw = o.MaxWidth - 2*pad
	}
	ch := max(int(math.Round(float64(ih)*float64(cw)/float64(iw))), 1)
	unit := float64(min(cw, ch))
	return Layout{
		Width: cw + 2*pad, Height: ch + 2*pad,
		ContentW: cw, ContentH: ch,
		Pad:        pad,
		Radius:     int(math.Round(min(unit*0.03, float64(pad)))),
		ShadowBlur: int(math.Round(float64(pad) * 0.6)),
		ShadowDY:   int(math.Round(float64(pad) * 0.12)),
	}
}

// backdrop is a rendered background: an image the size of the GIF with a
// transparent hole where the recording shows through.
type backdrop struct {
	path   string
	layout Layout
}

// makeBackdrop probes the size of in and renders its background to dir. It
// returns nil when opts has no background.
func makeBackdrop(ctx context.Context, ffmpeg, in, dir string, opts Options) (*backdrop, error) {
	if !opts.Background.Enabled() {
		return nil, nil
	}
	iw, ih, err := ProbeSize(ctx, ffmpeg, in)
	if err != nil {
		return nil, err
	}
	b := &backdrop{path: filepath.Join(dir, "background.png"), layout: opts.Layout(iw, ih)}
	if err := writePNG(b.path, renderBackdrop(b.layout, opts.Background.Gradient())); err != nil {
		return nil, fmt.Errorf("drawing background: %w", err)
	}
	return b, nil
}

// renderBackdrop draws the gradient with a shadow under the recording and a
// rounded, anti-aliased hole for it.
func renderBackdrop(l Layout, g Gradient) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, l.Width, l.Height))
	cos, sin := math.Cos(g.Angle*math.Pi/180), math.Sin(g.Angle*math.Pi/180)
	// Project the corners onto the gradient's direction to find its extent.
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, c := range [][2]float64{{0, 0}, {float64(l.Width), 0}, {0, float64(l.Height)}, {float64(l.Width), float64(l.Height)}} {
		p := c[0]*cos + c[1]*sin
		lo, hi = min(lo, p), max(hi, p)
	}

	cx, cy := float64(l.Width)/2, float64(l.Height)/2
	hw, hh, r := float64(l.ContentW)/2, float64(l.ContentH)/2, float64(l.Radius)
	blur := max(float64(l.ShadowBlur), 1)
	const shadowStrength = 0.28
	// Blending in HSL per pixel is slow; look the colours up instead.
	lut := make([]color.RGBA, 1024)
	for i := range lut {
		lut[i] = g.At(float64(i) / float64(len(lut)-1))
	}
	for y := range l.Height {
		for x := range l.Width {
			px, py := float64(x)+0.5, float64(y)+0.5
			c := lut[int(clamp01((px*cos+py*sin-lo)/(hi-lo))*float64(len(lut)-1))]

			ds := roundedRectDist(px-cx, py-cy-float64(l.ShadowDY), hw, hh, r)
			shade := 1 - shadowStrength*(1-smoothstep(-blur/2, blur, ds))
			hole := clamp01(0.5 - roundedRectDist(px-cx, py-cy, hw, hh, r)) // recording's coverage

			i := img.PixOffset(x, y)
			img.Pix[i+0] = uint8(float64(c.R) * shade)
			img.Pix[i+1] = uint8(float64(c.G) * shade)
			img.Pix[i+2] = uint8(float64(c.B) * shade)
			img.Pix[i+3] = uint8(math.Round(255 * (1 - hole)))
		}
	}
	return img
}

// roundedRectDist is the signed distance from (x, y) to a rectangle centred
// on the origin with half-size hw×hh and corner radius r: negative inside.
func roundedRectDist(x, y, hw, hh, r float64) float64 {
	qx, qy := math.Abs(x)-hw+r, math.Abs(y)-hh+r
	return math.Hypot(max(qx, 0), max(qy, 0)) + min(max(qx, qy), 0) - r
}

func smoothstep(a, b, x float64) float64 {
	t := clamp01((x - a) / (b - a))
	return t * t * (3 - 2*t)
}

func clamp01(x float64) float64 { return min(max(x, 0), 1) }

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)
	enc := png.Encoder{CompressionLevel: png.BestSpeed} // a temp file; speed matters more
	if err := enc.Encode(w, img); err != nil {
		_ = f.Close()
		return err
	}
	if err := w.Flush(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

var videoSizeRE = regexp.MustCompile(`Stream #.*: Video: .*?, (\d+)x(\d+)`)

// ProbeSize reads the dimensions of the video at in from ffmpeg's banner.
func ProbeSize(ctx context.Context, ffmpeg, in string) (w, h int, err error) {
	out, _ := exec.CommandContext(ctx, ffmpeg, "-hide_banner", "-nostdin", "-i", in).CombinedOutput()
	if err := ctx.Err(); err != nil {
		return 0, 0, err
	}
	w, h, ok := ParseSize(string(out))
	if !ok {
		return 0, 0, fmt.Errorf("reading %s: no video size found\n%s", filepath.Base(in), tail(string(out), 5))
	}
	return w, h, nil
}

// ParseSize extracts the first video stream's dimensions from ffmpeg's
// stderr banner.
func ParseSize(stderr string) (w, h int, ok bool) {
	m := videoSizeRE.FindStringSubmatch(stderr)
	if m == nil {
		return 0, 0, false
	}
	w, _ = strconv.Atoi(m[1])
	h, _ = strconv.Atoi(m[2])
	return w, h, w > 0 && h > 0
}

// hsl converts hue (degrees), saturation and lightness (0 to 1) to RGB.
func hsl(h, s, l float64) color.RGBA {
	h = math.Mod(math.Mod(h, 360)+360, 360)
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	m := l - c/2
	to8 := func(v float64) uint8 { return uint8(math.Round(clamp01(v+m) * 255)) }
	return color.RGBA{to8(r), to8(g), to8(b), 255}
}

// toHSL is the inverse of hsl.
func toHSL(c color.RGBA) (h, s, l float64) {
	r, g, b := float64(c.R)/255, float64(c.G)/255, float64(c.B)/255
	hi, lo := max(r, g, b), min(r, g, b)
	l = (hi + lo) / 2
	d := hi - lo
	if d == 0 {
		return 0, 0, l
	}
	s = d / (1 - math.Abs(2*l-1))
	switch hi {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	return math.Mod(h*60+360, 360), s, l
}
