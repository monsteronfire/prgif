package tui

import "charm.land/bubbles/v2/key"

type keyMap struct {
	Quit   key.Binding
	Cancel key.Binding
	Select key.Binding
	Up     key.Binding
	Down   key.Binding
	Left   key.Binding
	Right  key.Binding

	Record  key.Binding
	Stop    key.Binding
	Discard key.Binding
	Preview key.Binding
	Shuffle key.Binding

	Reveal   key.Binding
	Copy     key.Binding
	Tweak    key.Binding
	Again    key.Binding
	Settings key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Quit: key.NewBinding(
			key.WithKeys("q", "ctrl+c"),
			key.WithHelp("q", "quit"),
		),
		Cancel: key.NewBinding(
			key.WithKeys("q", "esc", "ctrl+c"),
			key.WithHelp("q/esc", "cancel"),
		),
		Select: key.NewBinding(
			key.WithKeys("s"),
			key.WithHelp("s", "select area"),
		),
		Up: key.NewBinding(
			key.WithKeys("up", "k"),
			key.WithHelp("↑/↓", "move"),
		),
		Down: key.NewBinding(
			key.WithKeys("down", "j"),
		),
		Left: key.NewBinding(
			key.WithKeys("left", "h"),
			key.WithHelp("←/→", "adjust"),
		),
		Right: key.NewBinding(
			key.WithKeys("right", "l"),
		),
		Record: key.NewBinding(
			key.WithKeys("enter"),
			key.WithHelp("enter", "record"),
		),
		Stop: key.NewBinding(
			key.WithKeys("enter", "space"),
			key.WithHelp("enter/space", "stop & make GIF"),
		),
		Discard: key.NewBinding(
			key.WithKeys("esc"),
			key.WithHelp("esc", "discard"),
		),
		Preview: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "preview"),
		),
		Shuffle: key.NewBinding(
			key.WithKeys("g"),
			key.WithHelp("g", "new colours"),
		),
		Reveal: key.NewBinding(
			key.WithKeys("o"),
			key.WithHelp("o", "reveal"),
		),
		Copy: key.NewBinding(
			key.WithKeys("c"),
			key.WithHelp("c", "copy path"),
		),
		Tweak: key.NewBinding(
			key.WithKeys("t"),
			key.WithHelp("t", "tweak"),
		),
		Again: key.NewBinding(
			key.WithKeys("r"),
			key.WithHelp("r", "record again"),
		),
		Settings: key.NewBinding(
			key.WithKeys("p"),
			key.WithHelp("p", "open privacy settings"),
		),
	}
}

// bindings is the subset of keys shown in the help line for one state.
type bindings []key.Binding

// ShortHelp implements help.KeyMap.
func (b bindings) ShortHelp() []key.Binding { return b }

// FullHelp implements help.KeyMap.
func (b bindings) FullHelp() [][]key.Binding { return [][]key.Binding{b} }

// helpBindings returns the keys that do something in the current state.
func (m Model) helpBindings() bindings {
	k := m.keys
	switch m.state {
	case stateSelecting, stateEncoding:
		return bindings{k.Cancel}
	case stateReady:
		return bindings{k.Record, withHelp(k.Select, "reselect"), k.Up, k.Left, k.Quit}
	case stateCountdown:
		return bindings{withHelp(k.Discard, "cancel")}
	case stateRecording:
		return bindings{k.Stop, k.Discard}
	case stateStopping:
		return nil
	case stateReview:
		// One entry for all four arrows keeps the line inside the panel.
		arrows := k.Up
		arrows.SetHelp("↑↓←→", "settings")
		return bindings{withHelp(k.Record, "save"), k.Preview, arrows, k.Discard, k.Quit}
	case statePreviewing:
		if m.previewCancelled {
			return nil
		}
		return bindings{k.Cancel}
	case stateError:
		b := bindings{withHelp(k.Select, "try again")}
		if m.permissionHint {
			b = append(b, k.Settings)
		}
		return append(b, k.Quit)
	case stateDone:
		return bindings{k.Reveal, k.Copy, k.Tweak, k.Again, k.Quit}
	case stateCancelling:
		return nil
	case stateIdle:
		return bindings{k.Select, k.Quit}
	default:
		return bindings{k.Quit}
	}
}

// withHelp returns a copy of b with a different help description.
func withHelp(b key.Binding, desc string) key.Binding {
	b.SetHelp(b.Help().Key, desc)
	return b
}
