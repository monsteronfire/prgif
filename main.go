package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/monsteronfire/prgif/internal/deps"
	"github.com/monsteronfire/prgif/internal/encoder"
	"github.com/monsteronfire/prgif/internal/tui"
)

const usage = `prgif: select an area of the screen, record it, and export an optimised GIF
for pull request descriptions.

Usage:
  prgif [flags]

Flags:
`

func main() {
	cfg := tui.Config{Encoder: encoder.DefaultOptions}
	flag.StringVar(&cfg.Output, "o", "", "output GIF `path` (default: prgif-YYYYMMDD-HHMMSS.gif in --dir)")
	dir := flag.String("dir", "~/Desktop", "`folder` to save GIFs in; falls back to the current folder if there's no Desktop")
	flag.IntVar(&cfg.Encoder.FPS, "fps", cfg.Encoder.FPS, "GIF frames per second (1-50)")
	flag.IntVar(&cfg.Encoder.MaxWidth, "width", cfg.Encoder.MaxWidth, "maximum GIF width in `pixels`; narrower recordings keep their size")
	flag.IntVar(&cfg.Encoder.Lossy, "lossy", cfg.Encoder.Lossy, "gifsicle lossy `level` (0 = off, up to 200)")
	flag.DurationVar(&cfg.Encoder.TrimEnd, "trim", cfg.Encoder.TrimEnd, "cut this `duration` from the end of recordings, e.g. 0.5s or 1s (0 = off; not applied to --encode)")
	flag.Float64Var(&cfg.Encoder.Speed, "speed", cfg.Encoder.Speed, "play the GIF this many `times` faster, from 1 to 4, e.g. 1.5")
	flag.IntVar(&cfg.Encoder.Background.Padding, "background", cfg.Encoder.Background.Padding, "frame the GIF on a random gradient, padded by this `percent` of its width (0 = off, up to 20)")
	flag.StringVar(&cfg.EncodeFile, "encode", "", "skip recording and open an existing `video` on the Review screen")
	flag.Usage = func() {
		w := flag.CommandLine.Output()
		_, _ = fmt.Fprint(w, usage)
		flag.PrintDefaults()
		_, _ = fmt.Fprintln(w, "\nfps, width, lossy, speed, trim and background can also be adjusted in the TUI, before recording and after it.")
	}
	flag.Parse()

	if flag.NArg() > 0 {
		usageError(fmt.Errorf("unexpected argument %q", flag.Arg(0)))
	}
	cfg.Encoder.Background = cfg.Encoder.Background.Shuffle() // new colours each run
	if err := cfg.Encoder.Validate(); err != nil {
		usageError(err)
	}

	dirSet, outputSet := false, false
	flag.Visit(func(f *flag.Flag) {
		dirSet = dirSet || f.Name == "dir"
		outputSet = outputSet || f.Name == "o"
	})
	if dirSet && outputSet {
		usageError(errors.New("use either -o or --dir, not both"))
	}
	home, _ := os.UserHomeDir()
	var err error
	if cfg.OutputDir, err = outputDir(*dir, dirSet, home); err != nil {
		usageError(err)
	}
	if cfg.EncodeFile != "" {
		if _, err := os.Stat(cfg.EncodeFile); err != nil {
			fail(err)
		}
	}

	if _, err := tea.NewProgram(tui.New(deps.Check(), cfg)).Run(); err != nil {
		fail(err)
	}
}

func usageError(err error) {
	fmt.Fprintln(os.Stderr, "prgif:", err)
	fmt.Fprintln(os.Stderr, "Run prgif -h for usage.")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "prgif:", err)
	os.Exit(1)
}
