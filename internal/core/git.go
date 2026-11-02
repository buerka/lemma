package core

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Git runs git commands in a repository. The implementation shells out to git
// intentionally: porcelain output and exit codes are the contract.
type Git struct {
	Dir string
}

func IsGitRepo(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, ".git"))
	return err == nil
}

func (g Git) run(args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func (g Git) Init() error {
	if IsGitRepo(g.Dir) {
		return nil
	}
	if err := os.MkdirAll(g.Dir, 0o755); err != nil {
		return err
	}
	if out, err := g.run("init", "-b", "main"); err != nil {
		return fmt.Errorf("git init: %s: %w", out, err)
	}
	return nil
}

func (g Git) Head() (string, error) {
	out, err := g.run("rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return out, nil
}

func (g Git) HeadOrEmpty() string {
	h, err := g.Head()
	if err != nil {
		return ""
	}
	return h
}

func (g Git) IsClean() (bool, error) {
	out, err := g.run("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return false, fmt.Errorf("git status: %s: %w", out, err)
	}
	return strings.TrimSpace(out) == "", nil
}

func (g Git) Dirty() ([]string, error) {
	out, err := g.run("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return nil, fmt.Errorf("git status: %s: %w", out, err)
	}
	if strings.TrimSpace(out) == "" {
		return nil, nil
	}
	return strings.Split(out, "\n"), nil
}

// Commit stages everything in the progress repository and commits it. It
// returns the new HEAD and whether a commit was actually created. If git has no
// identity configured, it retries with a local synthetic identity so the
// ledger still works in CI and throwaway containers.
func (g Git) Commit(message string) (string, bool, error) {
	if out, err := g.run("add", "-A"); err != nil {
		return "", false, fmt.Errorf("git add: %s: %w", out, err)
	}
	// Nothing staged: report the current HEAD instead of creating an empty
	// commit.
	diffOut, err := g.run("diff", "--cached", "--quiet")
	if err == nil {
		head := g.HeadOrEmpty()
		return head, false, nil
	}
	_ = diffOut

	out, err := g.run("commit", "--quiet", "-m", message)
	if err != nil {
		lower := strings.ToLower(out)
		if strings.Contains(lower, "author identity unknown") || strings.Contains(lower, "please tell me who you are") || strings.Contains(lower, "unable to auto-detect email") {
			out2, err2 := g.runWithConfig(message)
			if err2 != nil {
				return "", false, fmt.Errorf("git commit: %s: %w", out2, err2)
			}
			out = out2
			err = nil
		}
	}
	if err != nil {
		return "", false, fmt.Errorf("git commit: %s: %w", out, err)
	}
	head, err := g.Head()
	if err != nil {
		return "", false, err
	}
	return head, true, nil
}

func (g Git) runWithConfig(message string) (string, error) {
	cmd := exec.Command("git", "-C", g.Dir,
		"-c", "user.name=lem",
		"-c", "user.email=lem@localhost",
		"commit", "--quiet", "-m", message)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return strings.TrimSpace(out.String()), err
}

func (g Git) ResetIndex() {
	_, _ = g.run("reset", "--quiet")
}

func (g Git) CommitExists(sha string) bool {
	if sha == "" {
		return false
	}
	_, err := g.run("cat-file", "-e", sha+"^{commit}")
	return err == nil
}

func (g Git) ShowCommit(sha string) (string, error) {
	return g.run("show", "--stat", "--oneline", "--no-renames", sha)
}

// TopLevel returns the repository root for dir, or "" if dir is not in a git
// repository.
func GitTopLevel(dir string) string {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return ""
	}
	return strings.TrimSpace(out.String())
}
