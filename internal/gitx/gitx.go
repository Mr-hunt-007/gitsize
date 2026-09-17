// Package gitx is the thin boundary between gitsize and the git CLI.
package gitx

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// ErrGitNotFound is returned when no git executable is on PATH.
var ErrGitNotFound = errors.New("git executable not found on PATH")

// Runner runs git commands against one repository directory.
type Runner struct {
	Dir string
}

// baseArgs are prepended to every invocation so user config cannot change the
// byte format we parse (colour codes, quoted paths).
var baseArgs = []string{"-c", "color.ui=never", "-c", "core.quotepath=false"}

func (r Runner) command(args ...string) *exec.Cmd {
	full := append(append([]string{}, baseArgs...), args...)
	cmd := exec.Command("git", full...)
	cmd.Dir = r.Dir
	cmd.Env = append(os.Environ(),
		"GIT_OPTIONAL_LOCKS=0",  // never take the index lock
		"GIT_NO_LAZY_FETCH=1",   // never download missing objects in partial clones
		"GIT_TERMINAL_PROMPT=0", // never prompt
		"GIT_PAGER=cat",
		"LC_ALL=C",
	)
	return cmd
}

// Error carries git's stderr alongside the exit error.
type Error struct {
	Args   []string
	Stderr string
	Err    error
}

func (e *Error) Error() string {
	msg := strings.TrimSpace(e.Stderr)
	if msg == "" {
		msg = e.Err.Error()
	}
	return fmt.Sprintf("git %s: %s", strings.Join(e.Args, " "), msg)
}

func (e *Error) Unwrap() error { return e.Err }

// Output runs git and returns stdout.
func (r Runner) Output(args ...string) ([]byte, error) {
	cmd := r.command(args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, ErrGitNotFound
		}
		return stdout.Bytes(), &Error{Args: args, Stderr: stderr.String(), Err: err}
	}
	return stdout.Bytes(), nil
}

// Stream runs git, hands stdout to consume while it runs, and waits for exit.
// If stdin is non-nil it is copied to the process concurrently.
func (r Runner) Stream(stdin func(io.Writer) error, consume func(io.Reader) error, args ...string) error {
	cmd := r.command(args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var in io.WriteCloser
	if stdin != nil {
		if in, err = cmd.StdinPipe(); err != nil {
			return err
		}
	}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return ErrGitNotFound
		}
		return err
	}
	writeErr := make(chan error, 1)
	if stdin != nil {
		go func() {
			e := stdin(in)
			if cerr := in.Close(); e == nil {
				e = cerr
			}
			writeErr <- e
		}()
	} else {
		writeErr <- nil
	}
	consumeErr := consume(out)
	if consumeErr != nil {
		// Drain so the process can exit instead of blocking on a full pipe.
		_, _ = io.Copy(io.Discard, out)
	}
	werr := <-writeErr
	waitErr := cmd.Wait()
	if waitErr != nil {
		return &Error{Args: args, Stderr: stderr.String(), Err: waitErr}
	}
	if consumeErr != nil {
		return consumeErr
	}
	return werr
}

// Version is a parsed git version.
type Version struct{ Major, Minor, Patch int }

func (v Version) String() string { return fmt.Sprintf("%d.%d.%d", v.Major, v.Minor, v.Patch) }

// AtLeast reports whether v >= major.minor.
func (v Version) AtLeast(major, minor int) bool {
	if v.Major != major {
		return v.Major > major
	}
	return v.Minor >= minor
}

var versionRe = regexp.MustCompile(`git version (\d+)\.(\d+)(?:\.(\d+))?`)

// ParseVersion parses `git version` output such as
// "git version 2.45.1.windows.1" or "git version 2.50.1 (Apple Git-155)".
func ParseVersion(s string) (Version, error) {
	m := versionRe.FindStringSubmatch(s)
	if m == nil {
		return Version{}, fmt.Errorf("cannot parse git version from %q", strings.TrimSpace(s))
	}
	var v Version
	v.Major, _ = strconv.Atoi(m[1])
	v.Minor, _ = strconv.Atoi(m[2])
	if m[3] != "" {
		v.Patch, _ = strconv.Atoi(m[3])
	}
	return v, nil
}

// GitVersion runs `git version`.
func GitVersion() (Version, error) {
	out, err := Runner{}.Output("version")
	if err != nil {
		return Version{}, err
	}
	return ParseVersion(string(out))
}
