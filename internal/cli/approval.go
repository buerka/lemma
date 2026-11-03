package cli

import (
	"bufio"
	"crypto/subtle"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/buerka/lemma/internal/core"
)

// authorizeApproval enforces the human gate in front of core.Approve.
//
// This is a workflow constraint, not a cryptographic proof: an operator with
// write access can still forge gates.jsonl. It does prevent an agent running
// non-interactively from approving its own changes by typing "--by human".
func authorizeApproval(c *Context, l *core.Ledger, ref, token string) (method, hash string, err error) {
	_, cs, err := core.FindChangeset(l.Dir, ref)
	if err != nil {
		return "", "", err
	}
	got, err := core.ComputeChangesetHash(*cs)
	if err != nil {
		return "", "", core.Wrap(core.CodeGeneral, "hash changeset", err)
	}
	if got != cs.Hash {
		return "", "", core.Errorf(core.CodeConflict, "changeset hash mismatch: recorded %s, computed %s; re-plan", cs.Hash, got)
	}

	if token != "" {
		path := filepath.Join(core.ConfigDir(), "approval-token")
		info, err := os.Stat(path)
		if err != nil {
			return "", "", core.Errorf(core.CodeGeneral, "approval token file not readable: %s", path)
		}
		if !info.Mode().IsRegular() {
			return "", "", core.Errorf(core.CodeGeneral, "approval token file is not a regular file: %s", path)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return "", "", core.Errorf(core.CodeGeneral, "approval token file must not be group/world accessible (chmod 600): %s", path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return "", "", core.Wrap(core.CodeGeneral, "read approval token", err)
		}
		want := strings.TrimSpace(string(b))
		if want == "" {
			return "", "", core.Errorf(core.CodeGeneral, "approval token file is empty: %s", path)
		}
		if subtle.ConstantTimeCompare([]byte(want), []byte(token)) != 1 {
			return "", "", core.Errorf(core.CodeGeneral, "approval token does not match %s", path)
		}
		return "token", got, nil
	}

	if !c.IsTTY {
		return "", "", core.Errorf(core.CodeGeneral,
			"approval requires an interactive TTY; for non-interactive approval, create %s with mode 0600 and pass --token <value>",
			filepath.Join(core.ConfigDir(), "approval-token"))
	}

	hexHash := strings.TrimPrefix(got, "sha256:")
	if len(hexHash) < 12 {
		return "", "", core.Errorf(core.CodeGeneral, "changeset hash is too short to confirm")
	}
	short := hexHash[:12]
	w := c.Out
	if c.Globals.JSON {
		w = c.Err
	}
	fmt.Fprintf(w, "changeset: %s\n", cs.ChangesetID)
	fmt.Fprintf(w, "hash:      sha256:%s...\n", short)
	fmt.Fprintf(w, "Type the first 12 hex characters to approve, or press Enter to cancel: ")
	line, readErr := bufio.NewReader(c.In).ReadString('\n')
	fmt.Fprintln(w)
	if strings.TrimSpace(line) == "" {
		if readErr != nil {
			return "", "", core.Errorf(core.CodeGeneral, "approval cancelled")
		}
		return "", "", core.Errorf(core.CodeGeneral, "approval cancelled: no confirmation entered")
	}
	if strings.TrimSpace(line) != short {
		return "", "", core.Errorf(core.CodeGeneral, "approval not confirmed: hash prefix mismatch")
	}
	return "tty", got, nil
}
