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
			done <- mcptools.New("0.3.0", allow).Serve(context.Background(), inR, outW)
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
		// gitsize has no destructive tools, so --allow-destructive changes nothing.
		if len(tools) != 2 || tools[1].(map[string]any)["name"] != "gitsize_svg" {
			t.Fatalf("allow=%v: tools = %v", allow, tools)
		}
		svgTool := tools[1].(map[string]any)
		svgAnn := svgTool["annotations"].(map[string]any)
		if svgAnn["readOnlyHint"] != false || svgAnn["destructiveHint"] != false {
			t.Errorf("gitsize_svg annotations = %v", svgAnn)
		}
		for name, p := range svgTool["inputSchema"].(map[string]any)["properties"].(map[string]any) {
			if d, _ := p.(map[string]any)["description"].(string); d == "" {
				t.Errorf("gitsize_svg property %s has no description", name)
			}
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

		svgOut := filepath.Join(t.TempDir(), "e2e.svg")
		send(`{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"gitsize_svg","arguments":{"dir":` + quote(dir) + `,"output":` + quote(svgOut) + `}}}`)
		res = recv()["result"].(map[string]any)
		if res["isError"] != nil || res["structuredContent"].(map[string]any)["path"] != svgOut {
			t.Errorf("gitsize_svg call = %v", res)
		}
		if b, err := os.ReadFile(svgOut); err != nil || !strings.Contains(string(b), "dump &amp; backup.sql") {
			t.Errorf("gitsize_svg file: %v", err)
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

// svg calls the gitsize_svg handler directly.
func svg(t *testing.T, args map[string]any) (mcptools.SVGResult, error) {
	t.Helper()
	raw, _ := json.Marshal(args)
	res, err := mcptools.New("test", false).Tools[1].Handler(context.Background(), raw)
	if err != nil {
		return mcptools.SVGResult{}, err
	}
	var r mcptools.SVGResult
	if err := json.Unmarshal([]byte(res.Text), &r); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, res.Text)
	}
	return r, nil
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(old) })
}

func TestSVGWritesAndReturnsAbsolutePath(t *testing.T) {
	dir := fixtureRepo(t)
	out := t.TempDir()
	chdir(t, out)

	r, err := svg(t, map[string]any{"dir": dir})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Base(dir) + "-gitsize.svg"
	if !filepath.IsAbs(r.Path) || filepath.Base(r.Path) != want || r.Replaced || r.Depth != 2 || r.Sort != "disk" {
		t.Errorf("result = %+v", r)
	}
	if r.InHeadDisk+r.OldDisk+r.DeletedDisk != r.BlobDiskBytes || r.DeletedDisk < 1<<20 {
		t.Errorf("split does not add up: %+v", r)
	}
	b, err := os.ReadFile(filepath.Join(out, want))
	if err != nil {
		t.Fatal(err)
	}
	if len(b) != r.Bytes || !strings.Contains(string(b), "depth 2") || !strings.Contains(string(b), ">deleted</tspan>") {
		t.Errorf("unexpected SVG (%d bytes, result says %d)", len(b), r.Bytes)
	}

	// Its own file is replaced; depth and sort apply.
	r, err = svg(t, map[string]any{"dir": dir, "output": want, "depth": 1, "sort": "size"})
	if err != nil || !r.Replaced {
		t.Fatalf("second write: %+v, %v", r, err)
	}
	if b, _ := os.ReadFile(filepath.Join(out, want)); !strings.Contains(string(b), "bars by uncompressed size, depth 1") {
		t.Error("file was not rewritten with depth 1 and sort size")
	}

	// A relative output resolves against the working directory.
	os.Mkdir(filepath.Join(out, "docs"), 0o755)
	r, err = svg(t, map[string]any{"dir": dir, "output": filepath.Join("docs", "weight.SVG")})
	if err != nil {
		t.Fatal(err)
	}
	if fi1, err1 := os.Stat(r.Path); err1 != nil {
		t.Errorf("path %s: %v", r.Path, err1)
	} else if fi2, _ := os.Stat(filepath.Join(out, "docs", "weight.SVG")); !os.SameFile(fi1, fi2) {
		t.Errorf("path = %s", r.Path)
	}
}

func TestSVGRefusals(t *testing.T) {
	dir := fixtureRepo(t)
	out := t.TempDir()
	foreign := filepath.Join(out, "logo.svg")
	os.WriteFile(foreign, []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`), 0o644)
	os.Mkdir(filepath.Join(out, "dir.svg"), 0o755)

	cases := []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"dir": dir, "output": foreign}, "not written by gitsize"},
		{map[string]any{"dir": dir, "output": filepath.Join(out, "dir.svg")}, "not a regular file"},
		{map[string]any{"dir": dir, "output": filepath.Join(out, "map.png")}, "must end in .svg"},
		{map[string]any{"dir": dir, "output": filepath.Join(out, "nope", "m.svg")}, "does not exist"},
		{map[string]any{"dir": t.TempDir(), "output": filepath.Join(out, "m.svg")}, "not a git repository"},
		{map[string]any{"dir": dir, "output": filepath.Join(out, "m.svg"), "depth": -1}, "depth must be 0"},
		{map[string]any{"dir": dir, "output": filepath.Join(out, "m.svg"), "sort": "fast"}, "sort must be"},
		{map[string]any{"dir": dir, "by": "path"}, "unknown"},
	}
	for _, c := range cases {
		_, err := svg(t, c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("args %v: err = %v, want %q", c.args, err, c.want)
		}
	}
	if b, _ := os.ReadFile(foreign); !strings.HasSuffix(string(b), "></svg>") {
		t.Error("a foreign SVG was modified")
	}
	if _, err := os.Stat(filepath.Join(out, "m.svg")); err == nil {
		t.Error("a failed call must not leave a file behind")
	}
}
