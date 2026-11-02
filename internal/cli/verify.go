package cli

import (
	"flag"
	"fmt"
	"strings"

	"github.com/buerka/lemma/internal/core"
)

func cmdVerify(c *Context, args []string) error {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	ok, err := parseFlags(c, fs, args, "Usage: lem verify [--json]\n")
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "verify takes no positional arguments")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	report, err := core.Verify(l, c.Globals.LockTimeout)
	if err != nil {
		return err
	}
	if c.Globals.JSON {
		if !report.OK {
			c.ExitCode = 1
		}
		return c.jsonOut(report)
	}
	if report.OK {
		fmt.Fprintf(c.Out, "OK %s: %d events, %d changesets, %d gates, %d receipts\n",
			report.Project, report.EventCount, report.ChangesetCount, report.GateCount, report.ReceiptCount)
		fmt.Fprintf(c.Out, "revision: %s\nhead: %s\ngit clean: %v\n", report.Revision, report.Head, report.GitClean)
		return nil
	}
	fmt.Fprintf(c.Err, "verify failed for %s (%d issue(s))\n", report.Project, len(report.Issues))
	for _, issue := range report.Issues {
		if issue.Path != "" {
			fmt.Fprintf(c.Err, "  [%s] %s (%s)\n", issue.Code, issue.Message, l.Rel(issue.Path))
		} else {
			fmt.Fprintf(c.Err, "  [%s] %s\n", issue.Code, issue.Message)
		}
	}
	c.ExitCode = 1
	return nil
}

func cmdArtifact(c *Context, args []string) error {
	if len(args) == 0 {
		return core.Errorf(core.CodeUsage, "artifact requires a subcommand: list, verify, fetch")
	}
	switch args[0] {
	case "list":
		return cmdArtifactList(c, args[1:])
	case "verify":
		return cmdArtifactVerify(c, args[1:])
	case "fetch":
		return core.Errorf(core.CodeNotImplemented, "artifact fetch is not implemented in v0.1 (remote artifact transfer is out of scope)")
	default:
		return core.Errorf(core.CodeUsage, "unknown artifact subcommand %q", args[0])
	}
}

func cmdArtifactList(c *Context, args []string) error {
	fs := flag.NewFlagSet("artifact list", flag.ContinueOnError)
	runID := fs.String("run", "", "run id")
	ok, err := parseFlags(c, fs, args, "Usage: lem artifact list [run_id] [--run ID]\n")
	if !ok {
		return err
	}
	if *runID == "" && fs.NArg() == 1 {
		*runID = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "artifact list accepts at most one run id")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	entries, err := core.ListArtifacts(l, *runID)
	if err != nil {
		return err
	}
	if c.Globals.JSON {
		return c.jsonOut(entries)
	}
	if len(entries) == 0 {
		fmt.Fprintln(c.Out, "no artifacts")
		return nil
	}
	fmt.Fprintln(c.Out, "RUN\tKIND\tNAME\tLOCAL\tSHA256\tURI/PATH")
	for _, e := range entries {
		uri := e.URI
		if uri == "" {
			uri = e.Path
		}
		fmt.Fprintf(c.Out, "%s\t%s\t%s\t%v\t%s\t%s\n", e.RunID, e.Kind, e.Name, e.Local, shortHash(e.SHA256), uri)
	}
	return nil
}

func cmdArtifactVerify(c *Context, args []string) error {
	fs := flag.NewFlagSet("artifact verify", flag.ContinueOnError)
	runID := fs.String("run", "", "run id")
	ok, err := parseFlags(c, fs, args, "Usage: lem artifact verify [run_id] [--run ID]\n")
	if !ok {
		return err
	}
	if *runID == "" && fs.NArg() == 1 {
		*runID = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "artifact verify accepts at most one run id")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	report, err := core.VerifyArtifacts(l, *runID, c.Globals.LockTimeout)
	if err != nil {
		return err
	}
	if c.Globals.JSON {
		if !report.OK {
			c.ExitCode = 1
		}
		return c.jsonOut(report)
	}
	for _, chk := range report.Checks {
		if chk.Status == "remote" {
			continue
		}
		fmt.Fprintf(c.Out, "%s  %s  %s\n", chk.Status, chk.RunID, chk.Path)
		if chk.Detail != "" && chk.Status != "ok" {
			fmt.Fprintf(c.Out, "      %s\n", chk.Detail)
		}
	}
	if report.OK {
		fmt.Fprintf(c.Out, "OK: %d check(s), %d failed\n", report.Checked, report.Failed)
		return nil
	}
	fmt.Fprintf(c.Err, "artifact verify failed: %d of %d check(s) failed\n", report.Failed, report.Checked)
	c.ExitCode = 1
	return nil
}

func shortHash(h string) string {
	if len(h) > 19 {
		return h[:19] + "…"
	}
	return h
}

func cmdMigrate(c *Context, args []string) error {
	fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
	ok, err := parseFlags(c, fs, args, "Usage: lem migrate\n")
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "migrate takes no arguments")
	}
	l, _, err := c.openLedger()
	if err != nil {
		// Open returns STALE for unsupported versions; there are no migrations
		// in v0.1, so surface that explicitly.
		if strings.Contains(err.Error(), "schema version") {
			return err
		}
		return err
	}
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{"schema_version": l.Project.SchemaVersion, "migrated": false})
	}
	fmt.Fprintf(c.Out, "ledger already uses schema version %d; nothing to migrate\n", l.Project.SchemaVersion)
	return nil
}
