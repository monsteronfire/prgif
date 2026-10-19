package tui

import "charm.land/lipgloss/v2"

var (
	colorAccent = lipgloss.Color("#FF5F87")
	colorMuted  = lipgloss.Color("#767676")
	colorText   = lipgloss.Color("#E4E4E4")
	colorWarn   = lipgloss.Color("#FFAF00")
	colorOK     = lipgloss.Color("#5FD787")
	colorError  = lipgloss.Color("#FF5F5F")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(colorAccent).
			Padding(0, 1)

	subtitleStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Italic(true)

	bodyStyle = lipgloss.NewStyle().
			Foreground(colorText)

	warnStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorWarn)

	okStyle = lipgloss.NewStyle().
		Foreground(colorOK)

	toolNameStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorText)

	hintStyle = lipgloss.NewStyle().
			Foreground(colorMuted)

	codeStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	headingOKStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorOK)

	errorStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorError)

	labelStyle = lipgloss.NewStyle().
			Foreground(colorMuted).
			Width(7)

	settingLabelStyle = lipgloss.NewStyle().
				Foreground(colorMuted).
				Width(11)

	settingLabelActiveStyle = settingLabelStyle.
				Foreground(colorText).
				Bold(true)

	settingValueStyle = lipgloss.NewStyle().
				Foreground(colorText).
				Width(6).
				Align(lipgloss.Center)

	recStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(colorError)

	countdownStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#FFFFFF")).
			Background(colorAccent).
			Padding(0, 2)

	spinnerStyle = lipgloss.NewStyle().
			Foreground(colorAccent)

	// contentWidth bounds wrapped text (error output, warnings) in the panel.
	contentWidth = 60

	panelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(colorAccent).
			Padding(1, 2)
)
