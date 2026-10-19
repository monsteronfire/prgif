package tui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// screenRecordingSettings opens System Settings at Privacy & Security →
// Screen Recording.
const screenRecordingSettings = "x-apple.systempreferences:com.apple.preference.security?Privacy_ScreenCapture"

// runCommand runs a macOS helper command (open, pbcopy) with optional stdin.
// Tests replace it.
var runCommand = func(stdin, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		if msg := strings.TrimSpace(string(out)); msg != "" {
			return fmt.Errorf("%s: %w: %s", name, err, msg)
		}
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// actionDoneMsg reports the outcome of a Done or Error screen action.
type actionDoneMsg struct {
	success string // notice to show on success
	failure string // notice prefix on failure
	err     error
}

func runAction(success, failure, stdin, name string, args ...string) tea.Cmd {
	return func() tea.Msg {
		return actionDoneMsg{success: success, failure: failure, err: runCommand(stdin, name, args...)}
	}
}

func revealInFinder(path string) tea.Cmd {
	return runAction("Revealed in Finder", "Couldn't reveal in Finder", "", "open", "-R", absPath(path))
}

func copyPath(path string) tea.Cmd {
	return runAction("Copied path to clipboard", "Couldn't copy the path", absPath(path), "pbcopy")
}

func openScreenRecordingSettings() tea.Cmd {
	return runAction("Opened Screen Recording settings", "Couldn't open System Settings", "", "open", screenRecordingSettings)
}

func absPath(p string) string {
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}

// availablePath returns p, or p with "-2", "-3"… before the extension if p
// already exists, so recording again with -o doesn't overwrite the last GIF.
func availablePath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	ext := filepath.Ext(p)
	base := strings.TrimSuffix(p, ext)
	for i := 2; ; i++ {
		candidate := fmt.Sprintf("%s-%d%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

// looksLikePermissionProblem reports whether a recording error is probably
// macOS refusing screen capture. ffmpeg's messages vary by macOS version, but
// they all come from its avfoundation input.
func looksLikePermissionProblem(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"avfoundation", "capture screen", "not authorized", "not permitted", "permission"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
