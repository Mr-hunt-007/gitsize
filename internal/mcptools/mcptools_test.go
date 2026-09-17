package mcptools_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mr-hunt-007/gitsize/internal/cli"
	"github.com/Mr-hunt-007/gitsize/internal/mcp"
	"github.com/Mr-hunt-007/gitsize/internal/mcptools"
)

func TestMain(m *testing.M) {
	// Isolate tests from the developer's git config (signing, hooks, autocrlf).
	dir, err := os.MkdirTemp("", "gitsize-mcp")
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

// fixtureRepo builds a repository where a 2 MiB dump was committed and then
// deleted, next to a few files that stay in HEAD.
func fixtureRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	git := func(date string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com",
			"-c", "commit.gpgsign=false", "-c", "core.autocrlf=false"}, args...)...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_AUTHOR_DATE="+date, "GIT_COMMITTER_DATE="+date)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	write := func(rel string, n int) {
		t.Helper()
		b := make([]byte, n)
		if _, err := rand.Read(b); err != nil {
			t.Fatal(err)
		}
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	git("2024-01-01T00:00:00Z", "init", "-q", "-b", "main")
	write("dump & backup.sql", 2<<20)
	write("assets/logo.png", 300<<10)
	write("src/app.js", 20<<10)
	git("2024-01-10T00:00:00Z", "add", "-A")
	git("2024-01-10T00:00:00Z", "commit", "-q", "-m", "first")
	if err := os.Remove(filepath.Join(dir, "dump & backup.sql")); err != nil {
		t.Fatal(err)
	}
	write("src/app.js", 25<<10)
	git("2024-03-02T00:00:00Z", "add", "-A")
	git("2024-03-02T00:00:00Z", "commit", "-q", "-m", "second")
	return dir
}

func call(t *testing.T, args any) (map[string]any, callResult) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatal(err)
	}
	res, err := report(context.Background(), raw)
	if err != nil {
		t.Fatalf("report(%s): %v", raw, err)
	}
	var m map[string]any
	if err := json.Unmarshal(res.Structured.(json.RawMessage), &m); err != nil {
		t.Fatalf("structured content is not JSON: %v", err)
	}
	return m, callResult{text: res.Text, structured: string(res.Structured.(json.RawMessage))}
}

type callResult struct{ text, structured string }

func notes(m map[string]any) string {
	var b strings.Builder
	for _, n := range m["notes"].([]any) {
		b.WriteString(n.(string))
		b.WriteByte('\n')
	}
	return b.String()
}

func TestReportMatchesCLIJSON(t *testing.T) {
	dir := fixtureRepo(t)
	got, res := call(t, map[string]any{"dir": dir, "largest": 1})
	if res.text != res.structured {
		t.Error("text and structured content differ")
	}

	var stdout, stderr bytes.Buffer
	if code := cli.Run([]string{dir, "--json", "--largest", "1"}, strings.NewReader(""), &stdout, &stderr); code != cli.ExitOK {
		t.Fatalf("cli exit %d: %s", code, stderr.String())
	}
	var want map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &want); err != nil {
		t.Fatal(err)
	}
	// Identical except for the notes the MCP handler adds.
	gotNotes := notes(got)
	delete(got, "notes")
	delete(want, "notes")
	// .git size can change between runs (index refresh), so compare the rest.
	delete(got["storage"].(map[string]any), "git_dir_bytes")
	delete(want["storage"].(map[string]any), "git_dir_bytes")
	gb, _ := json.Marshal(got)
	wb, _ := json.Marshal(want)
	if !bytes.Equal(gb, wb) {
		t.Errorf("MCP JSON differs from --json:\nmcp: %s\ncli: %s", gb, wb)
	}

	blobs := got["largest_blobs"].([]any)
	if len(blobs) != 1 || blobs[0].(map[string]any)["path"] != "dump & backup.sql" || blobs[0].(map[string]any)["status"] != "deleted" {
		t.Errorf("largest_blobs = %v", blobs)
	}
	if !strings.Contains(res.text, `"dump & backup.sql"`) {
		t.Error("text escapes HTML characters, unlike --json")
	}
	if !strings.Contains(gotNotes, "showing the largest 1 of 4 blob versions") || !strings.Contains(gotNotes, "Raise the largest argument") {
		t.Errorf("missing truncation note:\n%s", gotNotes)
	}
	if !strings.Contains(gotNotes, "suggestions only and were not run") {
		t.Errorf("missing fix safety note:\n%s", gotNotes)
	}
	fix := got["fix"].(map[string]any)
	if fix["filter_repo"] != `git filter-repo --invert-paths --path 'dump & backup.sql'` {
		t.Errorf("fix = %v", fix)
	}
}

func TestReportDefaultsAndModes(t *testing.T) {
	dir := fixtureRepo(t)
	tests := []struct {
		args    map[string]any
		list    string
		n       int
		history bool
	}{
		{map[string]any{"dir": dir}, "largest_blobs", 4, false},
		{map[string]any{"dir": dir, "by": "path", "sort": "size"}, "largest_paths", 3, false},
		{map[string]any{"dir": dir, "by": "EXT", "history": true}, "by_ext", 3, true},
		{map[string]any{"dir": dir, "by": "dir", "largest": 100}, "by_dir", 3, false},
	}
	for _, tt := range tests {
		m, _ := call(t, tt.args)
		rows, ok := m[tt.list].([]any)
		if !ok || len(rows) != tt.n {
			t.Errorf("%v: %s = %v", tt.args, tt.list, m[tt.list])
		}
		if _, has := m["history"]; has != tt.history {
			t.Errorf("%v: history present = %v", tt.args, has)
		}
		if strings.Contains(notes(m), "Truncated") {
			t.Errorf("%v: nothing was cut but got %s", tt.args, notes(m))
		}
		opts := m["options"].(map[string]any)
		if tt.args["largest"] == nil && opts["largest"].(float64) != mcptools.DefaultLargest {
			t.Errorf("default largest = %v", opts["largest"])
		}
	}
}

func TestReportRelativeAndDefaultDir(t *testing.T) {
	dir := fixtureRepo(t)
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(filepath.Join(dir, "src")); err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(wd)
	for _, args := range []map[string]any{{}, {"dir": ""}, {"dir": ".."}, {"dir": "."}} {
		m, _ := call(t, args)
		if name := m["repository"].(map[string]any)["name"]; name != filepath.Base(dir) {
			t.Errorf("%v: repository name %v", args, name)
		}
	}
}

func TestReportArgumentErrors(t *testing.T) {
	notRepo := t.TempDir()
	tests := []struct {
		args string
		want string
	}{
		{`{"largest": 0}`, "largest must be between 1 and 100"},
		{`{"largest": 101}`, "largest must be between 1 and 100"},
		{`{"largest": "ten"}`, "invalid arguments"},
		{`{"sort": "fast"}`, `sort must be "disk" or "size"`},
		{`{"by": "author"}`, `by must be "blob", "path", "ext" or "dir"`},
		{`{"path": "."}`, "unknown field"},
		{`{"dir": ` + quote(notRepo) + `}`, "not a git repository"},
		{`{"dir": ` + quote(filepath.Join(notRepo, "missing")) + `}`, "not a git repository"},
	}
	for _, tt := range tests {
		if strings.Contains(tt.want, "git repository") {
			if _, err := exec.LookPath("git"); err != nil {
				continue
			}
		}
		_, err := report(context.Background(), json.RawMessage(tt.args))
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.args, err, tt.want)
		}
	}
}

func TestReportCancelled(t *testing.T) {
	dir := fixtureRepo(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := report(ctx, json.RawMessage(`{"dir": `+quote(dir)+`}`))
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestFixTruncationNote(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := fixtureRepo(t)
	// A second deleted path over 1 MiB, so largest=1 cuts the fix list.
	b := make([]byte, 3<<20)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "video.mp4"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "video"}, {"rm", "-q", "video.mp4"}, {"commit", "-q", "-m", "rm video"}} {
		cmd := exec.Command("git", append([]string{"-c", "user.name=T", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	m, _ := call(t, map[string]any{"dir": dir, "largest": 1})
	if !strings.Contains(notes(m), "fix suggestion covers the largest 1 of 2 deleted paths") {
		t.Errorf("notes:\n%s", notes(m))
	}
	if paths := m["fix"].(map[string]any)["paths"].([]any); len(paths) != 1 || paths[0] != "video.mp4" {
		t.Errorf("fix paths = %v", paths)
	}
}

// report calls the gitsize_report handler directly.
func report(ctx context.Context, args json.RawMessage) (mcp.Result, error) {
	return mcptools.New("test", false).Tools[0].Handler(ctx, args)
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestServerEndToEnd drives the real server over pipes the way a client does.
func TestServerEndToEnd(t *testing.T) {
	dir := fixtureRepo(t)
	for _, allow := range []bool{false, true} {
		inR, inW := io.Pipe()
		outR, outW := io.Pipe()
		done := make(chan error, 1)
		go func() {
			done <- mcptools.New("0.2.0", allow).Serve(context.Background(), inR, outW)
			outW.Close()
		}()
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 4<<20), 4<<20)
		send := func(s string) {
			t.Helper()
			if _, err := io.WriteString(inW, s+"\n"); err != nil {
				t.Fatal(err)
			}
		}
		recv := func() map[string]any {
			t.Helper()
			if !sc.Scan() {
				t.Fatalf("no response: %v", sc.Err())
			}
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Fatalf("bad line %q: %v", sc.Text(), err)
			}
			return m
		}

		send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"0"}}}`)
		init := recv()["result"].(map[string]any)
		if init["serverInfo"].(map[string]any)["name"] != "gitsize" || !strings.Contains(init["instructions"].(string), "never rewrites history") {
			t.Errorf("initialize = %v", init)
		}
		send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)

		send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
		tools := recv()["result"].(map[string]any)["tools"].([]any)
		if len(tools) != 1 {
			t.Fatalf("allow=%v: tools = %v", allow, tools)
		}
		tool := tools[0].(map[string]any)
		ann := tool["annotations"].(map[string]any)
		if tool["name"] != "gitsize_report" || ann["readOnlyHint"] != true || ann["destructiveHint"] != false {
			t.Errorf("tool = %v", tool)
		}
		desc := tool["description"].(string)
		for _, want := range []string{"never rewrites history", "suggestions for a human to review"} {
			if !strings.Contains(desc, want) {
				t.Errorf("description lacks %q", want)
			}
		}
		for name, p := range tool["inputSchema"].(map[string]any)["properties"].(map[string]any) {
			if d, _ := p.(map[string]any)["description"].(string); d == "" {
				t.Errorf("property %s has no description", name)
			}
		}

		send(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"gitsize_report","arguments":{"dir":` + quote(dir) + `,"by":"path"}}}`)
		res := recv()["result"].(map[string]any)
		content := res["structuredContent"].(map[string]any)
		if res["isError"] != nil || content["largest_paths"].([]any)[0].(map[string]any)["path"] != "dump & backup.sql" {
			t.Errorf("call = %v", res)
		}

		send(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"gitsize_report","arguments":{"dir":` + quote(t.TempDir()) + `}}}`)
		res = recv()["result"].(map[string]any)
		if res["isError"] != true || !strings.Contains(res["content"].([]any)[0].(map[string]any)["text"].(string), "not a git repository") {
			t.Errorf("not-a-repo call = %v", res)
		}

		inW.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("server did not stop at EOF")
		}
	}
}
