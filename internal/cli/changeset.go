package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/buerka/lemma/internal/core"
)

func cmdPlan(c *Context, args []string) error {
	fs := flag.NewFlagSet("plan", flag.ContinueOnError)
	taskID := fs.String("task", "", "task id")
	status := fs.String("status", "", "target status: todo|doing|blocked|done")
	intent := fs.String("intent", "", "changeset intent")
	out := fs.String("out", "", "changeset output path (must be under progress/changesets/)")
	revert := fs.String("revert", "", "changeset id to revert")
	expected := fs.String("expected-revision", "", "require this current revision")
	var evidence, runs, risks stringList
	fs.Var(&evidence, "evidence", "evidence path (repeatable)")
	fs.Var(&runs, "run", "run id to attach (repeatable)")
	fs.Var(&risks, "risk", "risk note (repeatable)")
	usage := `Usage: lem plan --task T1 --status done [--evidence PATH]... [--run RUN_ID]...
       [--intent "..."] [--out progress/changesets/cs-YYYYMMDD-NNN.json]
       [--revert CS_ID] [--expected-revision HASH]

Creates a changeset against the current revision. Plan does not change task
state; a human must approve the changeset hash, then lem apply executes it.
`
	ok, err := parseFlags(c, fs, args, usage)
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "plan takes no positional arguments")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	outPath := *out
	if outPath != "" {
		outPath = normalizeChangesetOut(l, outPath)
	}
	cs, path, err := core.Plan(l, core.PlanOptions{
		TaskID: *taskID, Status: *status, Intent: *intent, Out: outPath,
		Revert: *revert, ExpectedRevision: *expected,
		Evidence: evidence, Runs: runs, Risks: risks,
		LockTimeout: c.Globals.LockTimeout,
	})
	if err != nil {
		return err
	}
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{"changeset": cs, "path": path})
	}
	fmt.Fprintf(c.Out, "planned %s\n", cs.ChangesetID)
	fmt.Fprintf(c.Out, "  path:          %s\n", l.Rel(path))
	fmt.Fprintf(c.Out, "  base revision: %s\n", cs.BaseRevision)
	fmt.Fprintf(c.Out, "  hash:          %s\n", cs.Hash)
	fmt.Fprintf(c.Out, "  intent:        %s\n", cs.Intent)
	fmt.Fprintln(c.Out, "\nReview and approve:")
	fmt.Fprintf(c.Out, "  lem review %s\n", l.Rel(path))
	fmt.Fprintf(c.Out, "  lem approve %s --by human\n", l.Rel(path))
	fmt.Fprintf(c.Out, "  lem apply %s\n", l.Rel(path))
	return nil
}

func normalizeChangesetOut(l *core.Ledger, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	cwd, _ := os.Getwd()
	candidate := cwd + string(os.PathSeparator) + p
	if core.WithinDir(l.ChangesetsDir(), candidate) {
		return candidate
	}
	rootCandidate := l.Root + string(os.PathSeparator) + p
	if core.WithinDir(l.ChangesetsDir(), rootCandidate) {
		return rootCandidate
	}
	return p
}

func cmdReview(c *Context, args []string) error {
	fs := flag.NewFlagSet("review", flag.ContinueOnError)
	expected := fs.String("expected-revision", "", "require this current revision")
	ok, err := parseFlags(c, fs, args, "Usage: lem review <changeset-id|path> [--expected-revision HASH]\n")
	if !ok {
		return err
	}
	if fs.NArg() != 1 {
		return core.Errorf(core.CodeUsage, "review requires exactly one changeset id or path")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	ref := resolveChangesetRef(l, fs.Arg(0))
	path, err := core.Review(l, ref, *expected, c.Globals.LockTimeout)
	if err != nil {
		return err
	}
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{"review": path})
	}
	fmt.Fprintf(c.Out, "wrote %s\n", l.Rel(path))
	fmt.Fprintln(c.Out, "\nRead it, then approve the hash explicitly:")
	fmt.Fprintln(c.Out, "  lem approve "+fs.Arg(0)+" --by human")
	return nil
}

func cmdApprove(c *Context, args []string) error {
	fs := flag.NewFlagSet("approve", flag.ContinueOnError)
	by := fs.String("by", "", "approver identity (for example: human)")
	gate := fs.String("gate", "", "gate label, for example G1")
	note := fs.String("note", "", "approval note")
	expected := fs.String("expected-revision", "", "require this current revision")
	ok, err := parseFlags(c, fs, args, "Usage: lem approve <changeset-id|path> --by human [--gate G1] [--note ...]\n")
	if !ok {
		return err
	}
	if fs.NArg() != 1 {
		return core.Errorf(core.CodeUsage, "approve requires exactly one changeset id or path")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	ref := resolveChangesetRef(l, fs.Arg(0))
	g, already, err := core.Approve(l, ref, core.ApproveOptions{
		By: *by, Gate: *gate, Note: *note, ExpectedRevision: *expected,
		LockTimeout: c.Globals.LockTimeout,
	})
	if err != nil {
		return err
	}
	if already {
		c.ExitCode = 5
	}
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{"gate": g, "already_approved": already})
	}
	if already {
		fmt.Fprintf(c.Err, "lem: changeset %s was already approved at this hash\n", g.ChangesetID)
	}
	fmt.Fprintf(c.Out, "approved %s at %s\n", g.ChangesetID, g.ChangesetHash)
	fmt.Fprintf(c.Out, "  by: %s\n  at: %s\n", g.ApprovedBy, g.ApprovedAt)
	if g.Gate != "" {
		fmt.Fprintf(c.Out, "  gate: %s\n", g.Gate)
	}
	fmt.Fprintln(c.Out, "\nApply:")
	fmt.Fprintf(c.Out, "  lem apply %s\n", fs.Arg(0))
	return nil
}

func cmdApply(c *Context, args []string) error {
	fs := flag.NewFlagSet("apply", flag.ContinueOnError)
	expected := fs.String("expected-revision", "", "require this current revision")
	ok, err := parseFlags(c, fs, args, "Usage: lem apply <changeset-id|path> [--expected-revision HASH]\n")
	if !ok {
		return err
	}
	if fs.NArg() != 1 {
		return core.Errorf(core.CodeUsage, "apply requires exactly one changeset id or path")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	ref := resolveChangesetRef(l, fs.Arg(0))
	res, err := core.Apply(l, ref, core.ApplyOptions{
		ExpectedRevision: *expected, LockTimeout: c.Globals.LockTimeout,
	})
	if err != nil {
		return err
	}
	if res.AlreadyApplied {
		c.ExitCode = 5
	}
	if c.Globals.JSON {
		return c.jsonOut(res)
	}
	if res.AlreadyApplied {
		fmt.Fprintf(c.Err, "lem: changeset %s was already applied\n", res.Receipt.ChangesetID)
	}
	fmt.Fprintf(c.Out, "applied %s\n", res.Receipt.ChangesetID)
	fmt.Fprintf(c.Out, "  changeset hash: %s\n", res.Receipt.ChangesetHash)
	fmt.Fprintf(c.Out, "  revision:       %s\n", res.Receipt.Revision)
	fmt.Fprintf(c.Out, "  git commit:     %s\n", res.Receipt.GitCommit)
	fmt.Fprintf(c.Out, "  receipt:        %s\n", l.Rel(l.ReceiptPath(res.Receipt.ChangesetID)))
	return nil
}
