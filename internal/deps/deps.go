// Package deps checks that the external tools prgif shells out to are installed.
package deps

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Tool is an external program prgif needs.
type Tool struct {
	Name    string // executable name
	Purpose string // what prgif uses it for
	Install string // how to install it
}

// Required lists every external tool, in the order they are reported.
var Required = []Tool{
	{Name: "ffmpeg", Purpose: "screen capture and GIF encoding", Install: "brew install ffmpeg"},
	{Name: "gifsicle", Purpose: "GIF size optimisation", Install: "brew install gifsicle"},
	{Name: "select-region", Purpose: "drag-to-select screen overlay", Install: "make helper (on the Mac)"},
}

// Status is the result of looking up one Tool.
type Status struct {
	Tool
	Path string // resolved path; empty when missing
}

// Found reports whether the tool was located.
func (s Status) Found() bool { return s.Path != "" }

// Report is the outcome of checking all required tools.
type Report []Status

// Missing returns the tools that could not be found.
func (r Report) Missing() []Status {
	var missing []Status
	for _, s := range r {
		if !s.Found() {
			missing = append(missing, s)
		}
	}
	return missing
}

// OK reports whether every tool was found.
func (r Report) OK() bool { return len(r.Missing()) == 0 }

// Path returns the resolved path of the named tool, or "" if it is missing or unknown.
func (r Report) Path(name string) string {
	for _, s := range r {
		if s.Name == name {
			return s.Path
		}
	}
	return ""
}

// Check looks up every required tool. It first looks next to the running
// executable (where `make install` puts the helper), then on PATH.
func Check() Report {
	var dirs []string
	if exe, err := os.Executable(); err == nil {
		dirs = append(dirs, filepath.Dir(exe))
	}
	return check(Required, dirs, exec.LookPath)
}

func check(tools []Tool, dirs []string, lookPath func(string) (string, error)) Report {
	report := make(Report, 0, len(tools))
	for _, t := range tools {
		report = append(report, Status{Tool: t, Path: find(t.Name, dirs, lookPath)})
	}
	return report
}

func find(name string, dirs []string, lookPath func(string) (string, error)) string {
	for _, dir := range dirs {
		candidate := filepath.Join(dir, name)
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return candidate
		}
	}
	if p, err := lookPath(name); err == nil {
		return p
	}
	return ""
}
