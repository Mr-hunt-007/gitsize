package scan

import (
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mr-hunt-007/gitsize/internal/analyze"
)

func TestMain(m *testing.M) {
	// Isolate tests from the developer's git config (signing, hooks, autocrlf).
	dir, err := os.MkdirTemp("", "gitsize-cfg")
	if err != nil {
		panic(err)
	}
	cfg := filepath.Join(dir, "gitconfig")
	if err := os.WriteFile(cfg, nil, 0o644); err != nil {
		panic(err)
	}
	os.Setenv("GIT_CONFIG_GLOBAL", cfg)
	os.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

type repo struct {
	t   *testing.T
	dir string
}

func newRepo(t *testing.T, args ...string) *repo {
	t.Helper()
	requireGit(t)
	r := &repo{t: t, dir: t.TempDir()}
	r.git(append([]string{"init", "-q", "-b", "main"}, args...)...)
	return r
}

func (r *repo) gitEnv(env []string, args ...string) string {
	r.t.Helper()
	full := append([]string{"-c", "user.name=Test", "-c", "user.email=test@example.com",
		"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func (r *repo) git(args ...string) string {
	r.t.Helper()
	return r.gitEnv(nil, args...)
}

func (r *repo) write(rel string, data []byte) {
	r.t.Helper()
	p := filepath.Join(r.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *repo) remove(rel string) {
	r.t.Helper()
	r.git("rm", "-q", "--", rel)
}

// commit commits everything with a fixed date (RFC 3339) and returns the hash.
func (r *repo) commit(date, msg string) string {
	r.t.Helper()
	r.git("add", "-A")
	env := []string{"GIT_AUTHOR_DATE=" + date, "GIT_COMMITTER_DATE=" + date}
	r.gitEnv(env, "commit", "-q", "--allow-empty", "-m", msg)
	return strings.TrimSpace(r.git("rev-parse", "HEAD"))
}

func (r *repo) blobOID(rev, rel string) string {
	r.t.Helper()
	return strings.TrimSpace(r.git("rev-parse", rev+":"+rel))
}

func random(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return b
}

func run(t *testing.T, dir string, by string, sort analyze.SortKey, history bool) *Report {
	t.Helper()
	rep, err := Run(Config{Dir: dir, Largest: 10, Sort: sort, By: by, History: history})
	if err != nil {
		t.Fatalf("Run(%s): %v", dir, err)
	}
	return rep
}
