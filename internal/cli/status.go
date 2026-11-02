package cli

import (
	"flag"
	"fmt"

	"github.com/buerka/lemma/internal/core"
	"github.com/buerka/lemma/internal/schema"
)

func cmdStatus(c *Context, args []string) error {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	ok, err := parseFlags(c, fs, args, "Usage: lem status [--json]\n")
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "status takes no positional arguments")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	lock, err := core.AcquireLock(l.Dir, c.Globals.LockTimeout)
	if err != nil {
		return err
	}
	summary, err := core.BuildSummary(l)
	if err != nil {
		_ = lock.Release()
		return err
	}
	if summary.GitClean {
		changed, err := l.WriteState(false)
		if err != nil {
			_ = lock.Release()
			return err
		}
		if changed {
			if _, _, err := l.Commit("state: refresh"); err != nil {
				_ = lock.Release()
				return core.Wrap(core.CodeGeneral, "commit state refresh", err)
			}
			summary, err = core.BuildSummary(l)
			if err != nil {
				_ = lock.Release()
				return err
			}
		}
	}
	_ = lock.Release()

	if c.Globals.JSON {
		return c.jsonOut(summary)
	}
	fmt.Fprintf(c.Out, "project:  %s\n", summary.Project)
	fmt.Fprintf(c.Out, "root:     %s\n", summary.Root)
	fmt.Fprintf(c.Out, "revision: %s\n", summary.Revision)
	fmt.Fprintf(c.Out, "phase:    %s\n", summary.Phase)
	fmt.Fprintf(c.Out, "tasks:    %d todo, %d doing, %d blocked, %d done\n",
		summary.Counts[schema.StatusTodo], summary.Counts[schema.StatusDoing],
		summary.Counts[schema.StatusBlocked], summary.Counts[schema.StatusDone])
	if len(summary.Active) > 0 {
		fmt.Fprint(c.Out, "active:   ")
		for i, t := range summary.Active {
			if i > 0 {
				fmt.Fprint(c.Out, ", ")
			}
			fmt.Fprintf(c.Out, "%s (%s)", t.ID, t.Title)
		}
		fmt.Fprintln(c.Out)
	}
	if len(summary.Blocked) > 0 {
		fmt.Fprint(c.Out, "blocked:  ")
		for i, t := range summary.Blocked {
			if i > 0 {
				fmt.Fprint(c.Out, ", ")
			}
			fmt.Fprintf(c.Out, "%s (%s)", t.ID, t.Title)
		}
		fmt.Fprintln(c.Out)
	}
	if summary.Next != nil {
		fmt.Fprintf(c.Out, "next:     %s — %s\n", summary.Next.ID, summary.Next.Title)
	} else {
		fmt.Fprintln(c.Out, "next:     none")
	}
	if len(summary.PendingChangesets) > 0 {
		fmt.Fprintln(c.Out, "pending changesets:")
		for _, p := range summary.PendingChangesets {
			state := "awaiting approval"
			if p.Approved {
				state = "approved, not applied"
			}
			fmt.Fprintf(c.Out, "  - %s (%s)\n", p.ID, state)
		}
	}
	if !summary.GitClean {
		fmt.Fprintln(c.Out, "\nwarning: progress/ has uncommitted changes; STATE.md was not refreshed")
	}
	fmt.Fprintf(c.Out, "\nstate file: %s\n", l.Rel(l.StatePath()))
	return nil
}

func cmdNext(c *Context, args []string) error {
	fs := flag.NewFlagSet("next", flag.ContinueOnError)
	ok, err := parseFlags(c, fs, args, "Usage: lem next [--json]\n")
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "next takes no positional arguments")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	tasks, err := l.LoadTasks()
	if err != nil {
		return err
	}
	next := core.NextTask(tasks)
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{"task": next})
	}
	if next == nil {
		fmt.Fprintln(c.Out, "no executable task")
		return nil
	}
	fmt.Fprintf(c.Out, "%s — %s\n", next.ID, next.Title)
	if next.Phase != "" {
		fmt.Fprintf(c.Out, "phase: %s\n", next.Phase)
	}
	if next.Owner != "" {
		fmt.Fprintf(c.Out, "owner: %s\n", next.Owner)
	}
	for _, a := range next.Acceptance {
		fmt.Fprintf(c.Out, "acceptance: %s\n", a)
	}
	return nil
}
