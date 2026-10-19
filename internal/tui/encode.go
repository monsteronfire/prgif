package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/monsteronfire/prgif/internal/encoder"
)

// encodeProgressMsg reports encoder progress.
type encodeProgressMsg encoder.Progress

// encodeDoneMsg is sent once when encoding finishes, fails or is cancelled.
type encodeDoneMsg struct {
	result encoder.Result
	err    error
}

// runEncode starts encoding in a goroutine and returns the channel its
// messages arrive on. The final message is always an encodeDoneMsg. Sends
// give up when ctx is cancelled so the goroutine never leaks.
func runEncode(ctx context.Context, tools encoder.Tools, in, out string, opts encoder.Options) <-chan tea.Msg {
	ch := make(chan tea.Msg)
	send := func(msg tea.Msg) {
		select {
		case ch <- msg:
		case <-ctx.Done():
		}
	}
	go func() {
		res, err := encoder.Encode(ctx, tools, in, out, opts, func(p encoder.Progress) {
			send(encodeProgressMsg(p))
		})
		// Deliver the result even after cancellation so the model can finish
		// quitting; the model keeps reading until it gets this message.
		ch <- encodeDoneMsg{result: res, err: err}
	}()
	return ch
}

// waitForEncode returns a command that delivers the next encoder message.
func waitForEncode(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// defaultOutputName is the GIF file name used when no -o flag is given.
func defaultOutputName(now time.Time) string {
	return now.Format("prgif-20060102-150405.gif")
}

// githubImageLimit is GitHub's maximum image size for PR descriptions and
// comments. Decimal megabytes, to err on the safe side.
const githubImageLimit = 10_000_000

// humanBytes formats a byte count for display in decimal units, matching
// Finder and GitHub's limit so "10.0 MB" means the same thing in both.
func humanBytes(n int64) string {
	const unit = 1000
	switch {
	case n < unit:
		return fmt.Sprintf("%d B", n)
	case n < unit*unit:
		return fmt.Sprintf("%.1f KB", float64(n)/unit)
	default:
		return fmt.Sprintf("%.1f MB", float64(n)/(unit*unit))
	}
}
