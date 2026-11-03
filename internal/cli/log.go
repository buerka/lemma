package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/buerka/lemma/internal/core"
	"github.com/buerka/lemma/internal/schema"
)

func cmdLog(c *Context, args []string) error {
	fs := flag.NewFlagSet("log", flag.ContinueOnError)
	taskID := fs.String("task", "", "filter by task id")
	typ := fs.String("type", "", "filter by event type")
	grep := fs.String("grep", "", "case-insensitive substring search")
	since := fs.String("since", "", "only events on/after YYYY-MM-DD or RFC3339")
	runID := fs.String("run", "", "filter by run id")
	changesetID := fs.String("changeset", "", "filter by changeset id")
	limit := fs.Int("limit", 50, "maximum events (0 = all)")
	ok, err := parseFlags(c, fs, args, "Usage: lem log [--task T] [--type TYPE] [--grep TEXT] [--since DATE] [--run ID] [--changeset ID] [--limit N]\n")
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "log takes no positional arguments")
	}
	var sinceTime time.Time
	if *since != "" {
		sinceTime, err = parseSince(*since)
		if err != nil {
			return core.Wrap(core.CodeUsage, "invalid --since", err)
		}
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	events, err := l.ReadEvents()
	if err != nil {
		return err
	}
	out := []schema.Event{}
	for _, ev := range events {
		if *taskID != "" && ev.TaskID != *taskID {
			continue
		}
		if *typ != "" && ev.Type != *typ {
			continue
		}
		if *runID != "" && ev.RunID != *runID {
			continue
		}
		if *changesetID != "" && ev.ChangesetID != *changesetID {
			continue
		}
		if !sinceTime.IsZero() {
			ts, err := time.Parse(time.RFC3339, ev.TS)
			if err != nil || ts.Before(sinceTime) {
				continue
			}
		}
		if *grep != "" {
			raw, _ := json.Marshal(ev)
			hay := strings.ToLower(string(raw) + " " + core.EventSummary(ev))
			if !strings.Contains(hay, strings.ToLower(*grep)) {
				continue
			}
		}
		out = append(out, ev)
	}
	if *limit > 0 && len(out) > *limit {
		out = out[len(out)-*limit:]
	}
	if c.Globals.JSON {
		return c.jsonOut(out)
	}
	if len(out) == 0 {
		fmt.Fprintln(c.Out, "no matching events")
		return nil
	}
	tw := tabwriter.NewWriter(c.Out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tTYPE\tTASK\tRUN\tCHANGESET\tSUMMARY")
	for _, ev := range out {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", ev.TS, ev.Type, ev.TaskID, ev.RunID, ev.ChangesetID, core.EventSummary(ev))
	}
	_ = tw.Flush()
	return nil
}

func parseSince(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	if t, err := time.ParseInLocation("2006-01-02", s, time.Local); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("expected YYYY-MM-DD or RFC3339, got %q", s)
}

func cmdShow(c *Context, args []string) error {
	fs := flag.NewFlagSet("show", flag.ContinueOnError)
	ok, err := parseFlags(c, fs, args, "Usage: lem show <changeset-id|receipt-id|path>\n")
	if !ok {
		return err
	}
	if fs.NArg() != 1 {
		return core.Errorf(core.CodeUsage, "show requires exactly one changeset or receipt")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	ref := resolveChangesetRef(l, fs.Arg(0))
	path, cs, csErr := core.FindChangeset(l.Dir, ref)
	if csErr == nil {
		got, err := core.ComputeChangesetHash(*cs)
		if err != nil {
			return core.Wrap(core.CodeGeneral, "hash changeset", err)
		}
		gates, err := l.ReadGates()
		if err != nil {
			return err
		}
		receipt, err := l.LoadReceipt(cs.ChangesetID)
		if err != nil {
			return err
		}
		gate := core.GateForHash(gates, cs.ChangesetID, cs.Hash)
		if c.Globals.JSON {
			return c.jsonOut(map[string]any{
				"kind": "changeset", "path": path, "hash_valid": got == cs.Hash,
				"changeset": cs, "gate": gate, "receipt": receipt,
			})
		}
		fmt.Fprintf(c.Out, "changeset: %s\n", cs.ChangesetID)
		fmt.Fprintf(c.Out, "project:   %s\n", cs.Project)
		fmt.Fprintf(c.Out, "intent:    %s\n", cs.Intent)
		fmt.Fprintf(c.Out, "base:      %s\n", cs.BaseRevision)
		fmt.Fprintf(c.Out, "hash:      %s\n", cs.Hash)
		fmt.Fprintf(c.Out, "valid:     %v\n", got == cs.Hash)
		fmt.Fprintf(c.Out, "path:      %s\n", l.Rel(path))
		for _, op := range cs.Operations {
			if op.Op == "run.append" {
				fmt.Fprintf(c.Out, "operation: run.append %s\n", op.RunID)
			} else {
				fmt.Fprintf(c.Out, "operation: %s %s %s -> %s\n", op.Op, op.ID, op.From, op.To)
			}
		}
		for _, e := range cs.Evidence {
			h, ok := l.EvidenceHash(e)
			status := "present"
			if !ok {
				status = "missing"
			}
			fmt.Fprintf(c.Out, "evidence:  %s %s (%s)\n", e, h, status)
		}
		if gate != nil {
			method := gate.ApprovalMethod
			if method == "" {
				method = "legacy"
			}
			fmt.Fprintf(c.Out, "approved:  yes, by %s at %s (method %s)\n", gate.ApprovedBy, gate.ApprovedAt, method)
		} else {
			fmt.Fprintln(c.Out, "approved:  no")
		}
		if receipt != nil {
			fmt.Fprintf(c.Out, "applied:   yes, revision %s, commit %s\n", receipt.Revision, receipt.GitCommit)
		} else {
			fmt.Fprintln(c.Out, "applied:   no")
		}
		return nil
	}

	receiptID := strings.TrimSuffix(filepath.Base(ref), ".json")
	receipt, err := l.LoadReceipt(receiptID)
	if err != nil {
		return err
	}
	if receipt == nil {
		return csErr
	}
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{"kind": "receipt", "receipt": receipt})
	}
	fmt.Fprintf(c.Out, "receipt:  %s\n", receipt.ChangesetID)
	fmt.Fprintf(c.Out, "project:  %s\n", receipt.Project)
	fmt.Fprintf(c.Out, "hash:     %s\n", receipt.ChangesetHash)
	fmt.Fprintf(c.Out, "applied:  %s\n", receipt.AppliedAt)
	fmt.Fprintf(c.Out, "revision: %s\n", receipt.Revision)
	fmt.Fprintf(c.Out, "commit:   %s\n", receipt.GitCommit)
	fmt.Fprintf(c.Out, "prev:     %s\n", receipt.PrevCommit)
	return nil
}
