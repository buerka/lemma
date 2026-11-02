package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// AtomicWrite writes data to path via a temp file in the same directory,
// fsyncs it, renames it over path, and fsyncs the directory.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()

	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Chmod(perm); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	cleanup = false
	return fsyncDir(dir)
}

func fsyncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

// AtomicAppendLine appends one JSONL record without exposing a partial line.
// It is used under the project lock, so the read-modify-write is safe.
func AtomicAppendLine(path string, line []byte) error {
	old, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if len(old) > 0 && old[len(old)-1] != '\n' {
		old = append(old, '\n')
	}
	next := make([]byte, 0, len(old)+len(line)+1)
	next = append(next, old...)
	next = append(next, line...)
	if len(line) == 0 || line[len(line)-1] != '\n' {
		next = append(next, '\n')
	}
	return AtomicWrite(path, next, 0o644)
}

// ResolvePath resolves p relative to root. Absolute paths are returned as-is.
// It also accepts paths that already include the ledger directory.
// EnsureClean refuses to write when the ledger repository has uncommitted
// changes. Every write path assumes a clean base so git add cannot absorb
// unrelated edits.
func EnsureClean(l *Ledger) error {
	return EnsureCleanExcept(l)
}

// EnsureCleanExcept is like EnsureClean but allows dirty paths under specific
// ledger-relative prefixes. It exists for run finish, where the runner is
// expected to drop remote_result.json into the run directory before the manager
// records it.
func EnsureCleanExcept(l *Ledger, allowedPrefixes ...string) error {
	dirty, err := l.Git().Dirty()
	if err != nil {
		return Wrap(CodeGeneral, "check ledger git status", err)
	}
	var disallowed []string
	for _, line := range dirty {
		rel := dirtyPath(line)
		ok := false
		for _, prefix := range allowedPrefixes {
			prefix = strings.TrimSuffix(filepath.ToSlash(prefix), "/")
			if rel == prefix || strings.HasPrefix(rel, prefix+"/") {
				ok = true
				break
			}
		}
		if !ok {
			disallowed = append(disallowed, line)
		}
	}
	if len(disallowed) > 0 {
		return Errorf(CodeGeneral, "progress/ has uncommitted changes outside the allowed run directory; commit or discard them first: %s", strings.Join(disallowed, "; "))
	}
	return nil
}

func dirtyPath(line string) string {
	path := line
	if len(line) > 3 {
		path = strings.TrimSpace(line[3:])
	}
	if idx := strings.Index(path, " -> "); idx >= 0 {
		path = strings.TrimSpace(path[idx+4:])
	}
	return filepath.ToSlash(strings.Trim(path, `"`))
}

func ResolvePath(root, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(root, p))
}

func WithinDir(dir, path string) bool {
	absDir, err1 := filepath.Abs(dir)
	absPath, err2 := filepath.Abs(path)
	if err1 != nil || err2 != nil {
		return false
	}
	rel, err := filepath.Rel(absDir, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func CleanupTemps(dir string) ([]string, error) {
	var removed []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".tmp-") {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove stale temp %s: %w", path, err)
			}
			removed = append(removed, path)
		}
		return nil
	})
	return removed, err
}
