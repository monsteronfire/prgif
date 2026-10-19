package deps

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func fakeLookPath(onPath ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, p := range onPath {
			if p == name {
				return "/usr/local/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
}

func TestCheckReportsMissingTools(t *testing.T) {
	report := check(Required, nil, fakeLookPath("gifsicle"))

	if report.OK() {
		t.Fatal("OK() = true, want false")
	}
	var missing []string
	for _, s := range report.Missing() {
		missing = append(missing, s.Name)
	}
	if want := []string{"ffmpeg", "select-region"}; !slices.Equal(missing, want) {
		t.Errorf("missing = %v, want %v", missing, want)
	}
	if got := report.Path("gifsicle"); got != "/usr/local/bin/gifsicle" {
		t.Errorf("Path(gifsicle) = %q", got)
	}
}

func TestCheckAllFound(t *testing.T) {
	report := check(Required, nil, fakeLookPath("ffmpeg", "gifsicle", "select-region"))
	if !report.OK() {
		t.Errorf("OK() = false, missing %v", report.Missing())
	}
}

func TestCheckPrefersExecutableDir(t *testing.T) {
	dir := t.TempDir()
	helper := filepath.Join(dir, "select-region")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A non-executable file must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "ffmpeg"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	report := check(Required, []string{dir}, fakeLookPath("select-region"))

	if got := report.Path("select-region"); got != helper {
		t.Errorf("Path(select-region) = %q, want %q", got, helper)
	}
	if got := report.Path("ffmpeg"); got != "" {
		t.Errorf("Path(ffmpeg) = %q, want missing", got)
	}
}
