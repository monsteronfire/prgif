package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// outputDir decides where timestamped GIFs are saved. An explicit --dir must
// be an existing directory. Without one, GIFs go to ~/Desktop like macOS
// screenshots, or the current directory ("") if there is no Desktop.
func outputDir(flagValue string, explicit bool, home string) (string, error) {
	if !explicit {
		desktop := filepath.Join(home, "Desktop")
		if home != "" && isDir(desktop) {
			return desktop, nil
		}
		return "", nil
	}

	dir := expandHome(flagValue, home)
	if !isDir(dir) {
		return "", fmt.Errorf("--dir %s is not an existing directory", flagValue)
	}
	return dir, nil
}

// expandHome replaces a leading ~ with home. The shell usually does this, but
// not in --dir=~/x.
func expandHome(p, home string) string {
	switch {
	case home == "":
		return p
	case p == "~":
		return home
	case strings.HasPrefix(p, "~/"):
		return filepath.Join(home, p[2:])
	}
	return p
}

func isDir(p string) bool {
	info, err := os.Stat(p)
	return err == nil && info.IsDir()
}
