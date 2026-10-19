package selector

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name string
		json string
		want Region
	}{
		{
			name: "retina",
			json: `{"displayID":1,"h":320,"scale":2,"screenIndex":0,"w":600,"x":412,"y":188}`,
			want: Region{X: 824, Y: 376, W: 1200, H: 640, DisplayID: 1, Scale: 2},
		},
		{
			name: "half points on retina",
			json: `{"displayID":1,"h":100.5,"scale":2,"screenIndex":0,"w":200.5,"x":10.5,"y":20.5}`,
			want: Region{X: 21, Y: 41, W: 400, H: 200, DisplayID: 1, Scale: 2},
		},
		{
			name: "non-retina odd size rounds down to even",
			json: `{"displayID":69733382,"h":301,"scale":1,"screenIndex":1,"w":641,"x":0,"y":0}`,
			want: Region{X: 0, Y: 0, W: 640, H: 300, DisplayID: 69733382, ScreenIndex: 1, Scale: 1},
		},
		{
			name: "helper that can draw a frame",
			json: `{"displayID":1,"frame":true,"h":320,"scale":2,"screenIndex":0,"w":600,"x":412,"y":188}`,
			want: Region{X: 824, Y: 376, W: 1200, H: 640, DisplayID: 1, Scale: 2, CanFrame: true},
		},
		{
			name: "integers are accepted",
			json: `{"displayID":2,"h":10,"scale":1,"screenIndex":1,"w":10,"x":5,"y":5}`,
			want: Region{X: 5, Y: 5, W: 10, H: 10, DisplayID: 2, ScreenIndex: 1, Scale: 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Parse([]byte(tt.json))
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Parse = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	for name, json := range map[string]string{
		"not json":        `nope`,
		"missing field":   `{"displayID":1,"h":320,"scale":2,"screenIndex":0,"w":600,"x":412}`,
		"zero scale":      `{"displayID":1,"h":320,"scale":0,"screenIndex":0,"w":600,"x":412,"y":188}`,
		"too small":       `{"displayID":1,"h":0.5,"scale":2,"screenIndex":0,"w":600,"x":412,"y":188}`,
		"negative x":      `{"displayID":1,"h":320,"scale":2,"screenIndex":0,"w":600,"x":-5,"y":188}`,
		"empty output":    ``,
		"wrong type":      `{"displayID":"main","h":320,"scale":2,"screenIndex":0,"w":600,"x":412,"y":188}`,
		"negative screen": `{"displayID":1,"h":320,"scale":2,"screenIndex":-1,"w":600,"x":412,"y":188}`,
		"zero dimension":  `{"displayID":1,"h":0,"scale":1,"screenIndex":1,"w":600,"x":0,"y":0}`,
	} {
		if _, err := Parse([]byte(json)); err == nil {
			t.Errorf("%s: Parse(%q) succeeded", name, json)
		}
	}
}

func TestParseOutdatedHelper(t *testing.T) {
	_, err := Parse([]byte(`{"displayID":1,"h":320,"scale":2,"w":600,"x":412,"y":188}`))
	if err == nil || !strings.Contains(err.Error(), "make helper") {
		t.Errorf("err = %v, want a rebuild hint", err)
	}
}

func TestRegionString(t *testing.T) {
	r := Region{X: 824, Y: 376, W: 1200, H: 640}
	if got, want := r.String(), "1200×640 at 824,376"; got != want {
		t.Errorf("String = %q, want %q", got, want)
	}
}

// fakeHelper writes a shell script standing in for select-region.
func fakeHelper(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "select-region")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRunSelected(t *testing.T) {
	helper := fakeHelper(t, `echo '{"displayID":1,"h":320,"scale":2,"screenIndex":0,"w":600,"x":412,"y":188}'`)

	got, err := Run(context.Background(), helper)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Region{X: 824, Y: 376, W: 1200, H: 640, DisplayID: 1, Scale: 2}); got != want {
		t.Errorf("Run = %+v, want %+v", got, want)
	}
}

func TestRunCancelled(t *testing.T) {
	helper := fakeHelper(t, `echo "select-region: cancelled" >&2; exit 1`)

	if _, err := Run(context.Background(), helper); !errors.Is(err, ErrCancelled) {
		t.Errorf("err = %v, want ErrCancelled", err)
	}
}

func TestRunFailureIncludesStderr(t *testing.T) {
	helper := fakeHelper(t, `echo "select-region: no displays found" >&2; exit 2`)

	_, err := Run(context.Background(), helper)
	if err == nil || errors.Is(err, ErrCancelled) || !strings.Contains(err.Error(), "no displays found") {
		t.Errorf("err = %v, want failure mentioning stderr", err)
	}
}

func TestRunContextCancelKillsHelper(t *testing.T) {
	helper := fakeHelper(t, `exec sleep 30`)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := Run(ctx, helper)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("Run took %v; helper was not killed", elapsed)
	}
}

func TestShowFrame(t *testing.T) {
	dir := t.TempDir()
	args := filepath.Join(dir, "args")
	// Record the arguments, then block until stdin closes, like the helper.
	helper := filepath.Join(dir, "select-region")
	script := "#!/bin/sh\necho \"$@\" > " + args + "\ncat > /dev/null\n"
	if err := os.WriteFile(helper, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	f, err := ShowFrame(helper, Region{X: 825, Y: 376, W: 1200, H: 640, DisplayID: 7, Scale: 2})
	if err != nil {
		t.Fatal(err)
	}
	var got []byte
	for deadline := time.Now().Add(5 * time.Second); len(got) == 0 && time.Now().Before(deadline); {
		got, _ = os.ReadFile(args)
		time.Sleep(10 * time.Millisecond)
	}
	done := make(chan struct{})
	go func() { f.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close didn't stop the helper")
	}

	// Pixels back to points for the helper.
	if want := "--frame 7 412.5 188 600 320\n"; string(got) != want {
		t.Errorf("helper args = %q, want %q", got, want)
	}
}
