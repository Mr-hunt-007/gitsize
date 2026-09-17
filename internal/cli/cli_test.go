package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	dir, _ := os.MkdirTemp("", "gitsize-cli")
	cfg := filepath.Join(dir, "gitconfig")
	_ = os.WriteFile(cfg, nil, 0o644)
	os.Setenv("GIT_CONFIG_GLOBAL", cfg)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func runCLI(args ...string) (int, string, string) {
	var out, errb bytes.Buffer
	code := Run(args, &out, &errb)
	return code, out.String(), errb.String()
}

func TestUsageAndVersion(t *testing.T) {
	tests := []struct {
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{[]string{"--version"}, ExitOK, "gitsize 0.1.0", ""},
		{[]string{"-h"}, ExitOK, "Usage:", ""},
		{[]string{"--help"}, ExitOK, "Exit codes:", ""},
		{[]string{"--bogus"}, ExitUsage, "", "flag provided but not defined"},
		{[]string{"--sort", "fast"}, ExitUsage, "", "--sort must be"},
		{[]string{"--by", "author"}, ExitUsage, "", "--by must be"},
		{[]string{"--largest", "0"}, ExitUsage, "", "--largest must be at least 1"},
		{[]string{"--largest", "x"}, ExitUsage, "", "invalid value"},
		{[]string{"a", "b"}, ExitUsage, "", "at most one repository path"},
	}
	for _, tt := range tests {
		code, out, errOut := runCLI(tt.args...)
		if code != tt.code || !strings.Contains(out, tt.stdout) || !strings.Contains(errOut, tt.stderr) {
			t.Errorf("%v: code=%d stdout=%q stderr=%q", tt.args, code, out, errOut)
		}
	}
}

func TestHelpHasNoEmDash(t *testing.T) {
	if strings.ContainsRune(usage, '\u2014') {
		t.Error("em dash in help text")
	}
}

func TestNotARepoExitCode(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	code, out, errOut := runCLI(t.TempDir())
	if code != ExitNotRepo || out != "" || !strings.Contains(errOut, "not a git repository") {
		t.Errorf("code=%d stdout=%q stderr=%q", code, out, errOut)
	}
}

func TestJSONOnRealRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	git("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "a file.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	git("add", "-A")
	git("commit", "-q", "-m", "init")

	// Flags after the positional path are accepted.
	code, out, errOut := runCLI(dir, "--json", "--by", "path", "--history")
	if code != ExitOK {
		t.Fatalf("code=%d stderr=%s", code, errOut)
	}
	var rep struct {
		Options struct{ By string } `json:"options"`
		Paths   []struct {
			Path   string `json:"path"`
			Status string `json:"status"`
		} `json:"largest_paths"`
		History *struct{ Months []any } `json:"history"`
	}
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, out)
	}
	if rep.Options.By != "path" || len(rep.Paths) != 1 || rep.Paths[0].Path != "a file.txt" || rep.Paths[0].Status != "in HEAD" {
		t.Errorf("unexpected: %+v", rep)
	}
	if rep.History == nil || len(rep.History.Months) != 1 {
		t.Errorf("history: %+v", rep.History)
	}

	// Text output to a non-TTY writer never contains colour codes.
	code, out, _ = runCLI(dir)
	if code != ExitOK || strings.Contains(out, "\x1b[") || !strings.Contains(out, "Largest blobs") {
		t.Errorf("text: code=%d\n%s", code, out)
	}
}
