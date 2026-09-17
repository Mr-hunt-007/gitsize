package gitx

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    Version
		wantErr bool
	}{
		{"git version 2.50.1 (Apple Git-155)\n", Version{2, 50, 1}, false},
		{"git version 2.45.1.windows.1\r\n", Version{2, 45, 1}, false},
		{"git version 2.31.0", Version{2, 31, 0}, false},
		{"git version 3.0", Version{3, 0, 0}, false},
		{"not git", Version{}, true},
	}
	for _, tt := range tests {
		got, err := ParseVersion(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%q: got %v, %v", tt.in, got, err)
		}
	}
}

func TestAtLeast(t *testing.T) {
	v := Version{2, 50, 1}
	cases := map[[2]int]bool{{2, 50}: true, {2, 31}: true, {2, 51}: false, {1, 99}: true, {3, 0}: false}
	for mm, want := range cases {
		if got := v.AtLeast(mm[0], mm[1]); got != want {
			t.Errorf("%v.AtLeast(%d.%d) = %v", v, mm[0], mm[1], got)
		}
	}
}

func TestRunnerErrors(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	r := Runner{Dir: t.TempDir()}
	_, err := r.Output("definitely-not-a-git-command")
	var ge *Error
	if !errors.As(err, &ge) || !strings.Contains(err.Error(), "definitely-not-a-git-command") {
		t.Errorf("err = %v", err)
	}
	if _, err := GitVersion(context.Background()); err != nil {
		t.Errorf("GitVersion: %v", err)
	}
	var out strings.Builder
	err = r.Stream(func(w io.Writer) error {
		_, err := io.WriteString(w, "hello\n")
		return err
	}, func(rd io.Reader) error {
		_, err := io.Copy(&out, rd)
		return err
	}, "hash-object", "--stdin")
	if err != nil || strings.TrimSpace(out.String()) != "ce013625030ba8dba906f756967f9e9ca394464a" {
		t.Errorf("Stream: %q, %v", out.String(), err)
	}
}

// TestStreamCancelKillsGit starts a git process that would read stdin
// forever and checks that cancelling the context kills it and returns.
func TestStreamCancelKillsGit(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	r := Runner{Dir: t.TempDir(), Ctx: ctx}
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- r.Stream(func(w io.Writer) error {
			buf := make([]byte, 64*1024)
			close(started)
			for {
				if _, err := w.Write(buf); err != nil {
					return err
				}
			}
		}, func(rd io.Reader) error {
			_, err := io.Copy(io.Discard, rd)
			return err
		}, "hash-object", "--stdin")
	}()
	<-started
	time.Sleep(100 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Stream did not return after cancel")
	}
	if _, err := r.Output("version"); !errors.Is(err, context.Canceled) {
		t.Errorf("Output with cancelled ctx: %v", err)
	}
}
