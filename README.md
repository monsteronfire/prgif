# TUI Gif Demo Maker

`prgif` is a terminal tool for making GIFs for pull request descriptions. Drag a rectangle on screen, record it, and get an optimised GIF that fits under GitHub's 10 MB image limit.

It runs on **macOS**. Development happens in the dev container, which can't see the Mac's screen, so the Go binary is cross-compiled in the container and run on the Mac.

## Requirements

**On the Mac**

- [Homebrew](https://brew.sh) packages: `brew install ffmpeg gifsicle`
- Xcode Command Line Tools, for building the overlay helper: `xcode-select --install`

**In the dev container**

- Go, ffmpeg and gifsicle are installed by the dev container setup.

## Build

There are two binaries, built in different places:

| Binary | What it is | Build where | Command |
| --- | --- | --- | --- |
| `bin/prgif-darwin-arm64` | The TUI (Go) | Dev container | `make build-darwin` |
| `bin/select-region` | The drag-to-select overlay (Swift/AppKit) | Mac | `make helper` |

1. In the dev container terminal:

   ```sh
   make build-darwin
   ```

2. In a terminal on the Mac, from the project folder:

   ```sh
   make helper
   ```

The project folder is shared, so both binaries end up side by side in `bin/`. `prgif` looks for `select-region` next to itself first, then on your `PATH`.

Rebuild the TUI after each Go change. Rebuild the helper only when `helper/select-region.swift` changes.

### Install

To run `prgif` from anywhere, install both binaries on the Mac after building them. Run this command in your Mac terminal, not the devcontainer:

```sh
make install
```

This copies them to `~/.local/bin` as `prgif` and `select-region`. Use `make install PREFIX=/usr/local` to install somewhere else. If the folder isn't on your `PATH`, `make install` says so.

## Run

On the Mac:

```sh
./bin/prgif-darwin-arm64
```

Or run `prgif` if you used `make install`. The start screen lists any missing tools with the command to install them.

**Screen Recording permission:** the first recording makes macOS ask to allow your terminal app under **System Settings → Privacy & Security → Screen Recording**. Grant it, then restart the terminal. If recording fails because of the permission, the error screen explains this, and `p` opens that settings page.

### Flags

| Flag | Default | Description |
| --- | --- | --- |
| `--dir folder` | `~/Desktop` | Folder to save GIFs in, e.g. `--dir .` for the current folder. Falls back to the current folder if there's no Desktop. |
| `-o path.gif` | | Save to this exact file instead of a timestamped name in `--dir`. Recording again in the same session saves to `path-2.gif`, `path-3.gif`… instead of overwriting. Can't be combined with `--dir`. |
| `--fps n` | 12 | GIF frames per second (1–50) |
| `--width px` | 800 | Maximum GIF width. Narrower recordings keep their size. |
| `--lossy n` | 80 | gifsicle lossy level, 0 (off) to 200 |
| `--speed n` | 1 | Play the GIF this many times faster, from 1 to 4, e.g. `1.5` |
| `--trim d` | 0.5s | How much to cut from the end of recordings, e.g. `1s`; `0` turns it off. Not applied to `--encode`. |
| `--background n` | 0 | Frame the GIF on a random gradient, padded by `n`% of its width (up to 20); `0` turns it off |
| `--encode video.mov` | | Skip selecting and recording, and open an existing video on the Review screen |

`--fps`, `--width`, `--lossy`, `--speed`, `--trim` and `--background` set the starting values; you can still adjust them in the TUI. Run `prgif -h` for the full list.

If encoding fails after a recording, the recording is kept and the error screen shows the `--encode` command to retry it.

## Development

Run these in the dev container:

```sh
make test   # unit and integration tests (integration tests use the container's ffmpeg and gifsicle)
make fmt    # gofumpt
make run    # run the TUI in the container (no screen capture there)
go run . --encode clip.mov   # try the encoding screens on a video in the container
```
