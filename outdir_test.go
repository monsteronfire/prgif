package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestOutputDirDefaultsToDesktop(t *testing.T) {
	home := t.TempDir()
	desktop := filepath.Join(home, "Desktop")
	if err := os.Mkdir(desktop, 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := outputDir("", false, home)
	if err != nil || got != desktop {
		t.Errorf("outputDir = %q, %v; want %q", got, err, desktop)
	}
}

func TestOutputDirWithoutDesktopUsesCurrentDir(t *testing.T) {
	for _, home := range []string{t.TempDir(), ""} {
		got, err := outputDir("", false, home)
		if err != nil || got != "" {
			t.Errorf("home %q: outputDir = %q, %v; want current directory", home, got, err)
		}
	}
}

func TestOutputDirExplicit(t *testing.T) {
	home := t.TempDir()
	gifs := filepath.Join(home, "gifs")
	if err := os.Mkdir(gifs, 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(home, "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		flag    string
		want    string
		wantErr bool
	}{
		{flag: gifs, want: gifs},
		{flag: "~/gifs", want: gifs},
		{flag: "~", want: home},
		{flag: ".", want: "."},
		{flag: filepath.Join(home, "missing"), wantErr: true},
		{flag: file, wantErr: true},
	}
	for _, tt := range tests {
		got, err := outputDir(tt.flag, true, home)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("outputDir(%q) = %q, %v; want %q, err %v", tt.flag, got, err, tt.want, tt.wantErr)
		}
	}
}
