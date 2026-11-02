package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buerka/lemma/internal/schema"
	tmpl "github.com/buerka/lemma/internal/templates"
)

type PlanOptions struct {
	TaskID           string
	Status           string
	Intent           string
	Out              string
	Revert           string
	ExpectedRevision string
	Evidence         []string
	Runs             []string
	Risks            []string
	LockTimeout      time.Duration
}

// Plan creates and commits a changeset against the current revision. It does
// not append an event: the revision only advances when the changeset is
// applied.
func Plan(l *Ledger, opts PlanOptions) (*schema.Changeset, string, error) {
	lock, err := AcquireLock(l.Dir, opts.LockTimeout)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = lock.Release() }()
	if err := EnsureClean(l); err != nil {
		return nil, "", err
	}

	rev, err := l.Revision()
	if err != nil {
		return nil, "", err
	}
	if opts.ExpectedRevision != "" && opts.ExpectedRevision != rev {
		return nil, "", revisionConflict(opts.ExpectedRevision, rev)
	}
	tasks, err := l.LoadTasks()
	if err != nil {
		return nil, "", err
	}

	var ops []schema.Operation
	var taskID, intent, rollback string
	var evidence, risks []string

	if opts.Revert != "" {
		targetPath, target, err := FindChangeset(l.Dir, opts.Revert)
		if err != nil {
			return nil, "", err
		}
		if target.Project != l.Project.Project {
			return nil, "", Errorf(CodeConflict, "changeset %s belongs to project %q", target.ChangesetID, target.Project)
		}
		receipt, err := l.LoadReceipt(target.ChangesetID)
		if err != nil {
			return nil, "", err
		}
		if receipt == nil || receipt.ChangesetHash != target.Hash {
			return nil, "", Errorf(CodeConflict, "changeset %s is not applied; nothing to revert", target.ChangesetID)
		}
		current := map[string]string{}
		for _, t := range tasks.Tasks {
			current[t.ID] = t.Status
		}
		var skipped int
		for i := len(target.Operations) - 1; i >= 0; i-- {
			op := target.Operations[i]
			switch op.Op {
			case "task.update":
				if current[op.ID] != op.To {
					return nil, "", Errorf(CodeConflict, "cannot revert %s: task %s is %q, expected %q", target.ChangesetID, op.ID, current[op.ID], op.To)
				}
				ops = append(ops, schema.Operation{Op: "task.update", ID: op.ID, From: op.To, To: op.From})
				current[op.ID] = op.From
			case "run.append":
				skipped++
			}
		}
		if len(ops) == 0 {
			return nil, "", Errorf(CodeConflict, "changeset %s has no reversible task operations", target.ChangesetID)
		}
		taskID = target.TaskID
		evidence = append(evidence, target.Evidence...)
		if opts.Intent != "" {
			intent = opts.Intent
		} else {
			intent = "revert " + target.ChangesetID
		}
		rollback = "history is append-only; revert this changeset with a new changeset"
		if skipped > 0 {
			risks = append(risks, fmt.Sprintf("%d run.append operation(s) are not reverted; run history is retained", skipped))
		}
		_ = targetPath
	} else {
		taskID = opts.TaskID
		if taskID == "" && len(opts.Runs) > 0 {
			spec, err := l.LoadRunSpec(opts.Runs[0])
			if err != nil {
				return nil, "", err
			}
			taskID = spec.TaskID
		}
		if taskID == "" {
			return nil, "", Errorf(CodeUsage, "plan requires --task (or --run)")
		}
		task := tasks.Find(taskID)
		if task == nil {
			return nil, "", Errorf(CodeNotFound, "task %s not found", taskID)
		}
		if opts.Status != "" {
			if !schema.ValidTaskStatus(opts.Status) {
				return nil, "", Errorf(CodeUsage, "invalid --status %q (todo|doing|blocked|done)", opts.Status)
			}
			if task.Status == opts.Status {
				return nil, "", Errorf(CodeConflict, "task %s is already %s", task.ID, opts.Status)
			}
			ops = append(ops, schema.Operation{Op: "task.update", ID: task.ID, From: task.Status, To: opts.Status})
		}
		for _, runID := range opts.Runs {
			if _, err := l.LoadRunSpec(runID); err != nil {
				return nil, "", err
			}
			ops = append(ops, schema.Operation{Op: "run.append", RunID: runID})
		}
		if len(ops) == 0 {
			return nil, "", Errorf(CodeUsage, "plan requires --status and/or --run")
		}
		for _, e := range opts.Evidence {
			if _, ok := l.EvidenceHash(e); !ok {
				return nil, "", Errorf(CodeNotFound, "evidence not found: %s", e)
			}
			evidence = append(evidence, l.NormalizeEvidence(e))
		}
		if opts.Status == schema.StatusDone && len(evidence) == 0 {
			return nil, "", Errorf(CodeUsage, "marking a task done requires at least one --evidence path")
		}
		if opts.Intent != "" {
			intent = opts.Intent
		} else if opts.Status != "" {
			intent = fmt.Sprintf("update %s status %s -> %s", task.ID, task.Status, opts.Status)
		} else {
			intent = "attach run evidence to " + task.ID
		}
		risks = append(risks, opts.Risks...)
		rollback = "lem plan --revert <this changeset id>"
	}

	now := l.clock()
	date := now.Format("20060102")
	var id, path string
	if opts.Out != "" {
		path, err = filepath.Abs(opts.Out)
		if err != nil {
			return nil, "", Wrap(CodeUsage, "resolve --out", err)
		}
		if !WithinDir(l.ChangesetsDir(), path) {
			return nil, "", Errorf(CodeUsage, "--out must be inside %s", l.ChangesetsDir())
		}
		var ok bool
		id, ok = ChangesetIDFromPath(path)
		if !ok {
			return nil, "", Errorf(CodeUsage, "changeset filename must be <id>.json with a valid id")
		}
		if _, err := os.Stat(path); err == nil {
			return nil, "", Errorf(CodeConflict, "changeset already exists: %s", path)
		}
	} else {
		id, err = NextChangesetID(l, date)
		if err != nil {
			return nil, "", err
		}
		path = filepath.Join(l.ChangesetsDir(), id+".json")
	}

	cs := &schema.Changeset{
		ChangesetID:  id,
		Project:      l.Project.Project,
		TaskID:       taskID,
		Intent:       intent,
		BaseRevision: rev,
		Operations:   ops,
		Evidence:     dedupe(evidence),
		Risks:        dedupe(risks),
		Rollback:     rollback,
		CreatedAt:    l.NowUTC(),
		PrevHash:     rev,
	}
	h, err := ComputeChangesetHash(*cs)
	if err != nil {
		return nil, "", Wrap(CodeGeneral, "hash changeset", err)
	}
	cs.Hash = h
	if err := SaveChangeset(path, cs); err != nil {
		return nil, "", err
	}
	if _, err := l.WriteState(true); err != nil {
		return nil, "", err
	}
	if _, _, err := l.Commit("plan: " + id + " " + intent); err != nil {
		return nil, "", Wrap(CodeGeneral, "commit changeset", err)
	}
	return cs, path, nil
}

// Review renders REVIEW.md for a changeset and commits it.
func Review(l *Ledger, ref, expectedRevision string, lockTimeout time.Duration) (string, error) {
	lock, err := AcquireLock(l.Dir, lockTimeout)
	if err != nil {
		return "", err
	}
	defer func() { _ = lock.Release() }()
	if err := EnsureClean(l); err != nil {
		return "", err
	}
	if err := CheckExpectedRevision(l, expectedRevision); err != nil {
		return "", err
	}

	path, cs, err := FindChangeset(l.Dir, ref)
	if err != nil {
		return "", err
	}
	got, err := ComputeChangesetHash(*cs)
	if err != nil {
		return "", Wrap(CodeGeneral, "hash changeset", err)
	}
	hashValid := got == cs.Hash
	if !hashValid {
		return "", Errorf(CodeConflict, "changeset hash mismatch: recorded %s, computed %s; re-plan", cs.Hash, got)
	}
	current, err := l.Revision()
	if err != nil {
		return "", err
	}
	data := tmpl.ReviewData{
		ChangesetID:     cs.ChangesetID,
		Project:         cs.Project,
		Intent:          cs.Intent,
		BaseRevision:    cs.BaseRevision,
		CurrentRevision: current,
		Hash:            cs.Hash,
		HashValid:       hashValid,
		Stale:           cs.BaseRevision != current,
		ChangesetPath:   l.Rel(path),
		Risks:           cs.Risks,
		Rollback:        cs.Rollback,
	}
	for _, op := range cs.Operations {
		target := op.ID
		if op.Op == "run.append" {
			target = op.RunID
		}
		data.Operations = append(data.Operations, tmpl.ReviewOperation{
			Op: op.Op, Target: target, From: op.From, To: op.To,
		})
	}
	for _, e := range cs.Evidence {
		h, ok := l.EvidenceHash(e)
		status := "present"
		if !ok {
			status = "missing"
		}
		data.Evidence = append(data.Evidence, tmpl.ReviewEvidence{Path: e, Hash: h, Status: status})
	}
	if data.Rollback == "" {
		data.Rollback = "n/a"
	}
	b, err := tmpl.RenderReview(data)
	if err != nil {
		return "", Wrap(CodeGeneral, "render REVIEW.md", err)
	}
	if err := AtomicWrite(l.ReviewPath(), b, 0o644); err != nil {
		return "", Wrap(CodeGeneral, "write REVIEW.md", err)
	}
	if _, _, err := l.Commit("review: " + cs.ChangesetID); err != nil {
		return "", Wrap(CodeGeneral, "commit review", err)
	}
	return l.ReviewPath(), nil
}

type ApproveOptions struct {
	By               string
	Gate             string
	Note             string
	ExpectedRevision string
	LockTimeout      time.Duration
}

// Approve records the exact changeset hash in gates.jsonl. It never mutates the
// changeset.
func Approve(l *Ledger, ref string, opts ApproveOptions) (*schema.Gate, bool, error) {
	if strings.TrimSpace(opts.By) == "" {
		return nil, false, Errorf(CodeUsage, "approve requires --by (for example --by human)")
	}
	lock, err := AcquireLock(l.Dir, opts.LockTimeout)
	if err != nil {
		return nil, false, err
	}
	defer func() { _ = lock.Release() }()
	if err := EnsureClean(l); err != nil {
		return nil, false, err
	}

	_, cs, err := FindChangeset(l.Dir, ref)
	if err != nil {
		return nil, false, err
	}
	got, err := ComputeChangesetHash(*cs)
	if err != nil {
		return nil, false, Wrap(CodeGeneral, "hash changeset", err)
	}
	if got != cs.Hash {
		return nil, false, Errorf(CodeConflict, "changeset hash mismatch: recorded %s, computed %s; re-plan", cs.Hash, got)
	}
	gates, err := l.ReadGates()
	if err != nil {
		return nil, false, err
	}
	if existing := GateForHash(gates, cs.ChangesetID, cs.Hash); existing != nil {
		return existing, true, nil
	}
	for _, g := range gates {
		if g.ChangesetID == cs.ChangesetID && g.ChangesetHash != cs.Hash {
			return nil, false, Errorf(CodeConflict, "changeset %s was approved at a different hash; changing it after approval is not allowed", cs.ChangesetID)
		}
	}
	rev, err := l.Revision()
	if err != nil {
		return nil, false, err
	}
	if opts.ExpectedRevision != "" && opts.ExpectedRevision != rev {
		return nil, false, revisionConflict(opts.ExpectedRevision, rev)
	}
	if cs.BaseRevision != rev {
		return nil, false, revisionConflict(cs.BaseRevision, rev)
	}
	g, err := l.AppendGate(schema.Gate{
		ChangesetID:   cs.ChangesetID,
		ChangesetHash: cs.Hash,
		ApprovedBy:    opts.By,
		Gate:          opts.Gate,
		Note:          opts.Note,
	})
	if err != nil {
		return nil, false, err
	}
	if _, _, err := l.Commit("approve: " + cs.ChangesetID + " by " + opts.By); err != nil {
		return nil, false, Wrap(CodeGeneral, "commit approval", err)
	}
	return &g, false, nil
}

type ApplyOptions struct {
	ExpectedRevision string
	LockTimeout      time.Duration
}

type ApplyResult struct {
	Receipt        schema.Receipt   `json:"receipt"`
	Changeset      schema.Changeset `json:"changeset"`
	Path           string           `json:"path"`
	AlreadyApplied bool             `json:"already_applied"`
}

func revisionConflict(expected, current string) error {
	return Errorf(CodeConflict, "revision conflict: expected %s, current %s; re-read lem status and re-plan", expected, current)
}

// CheckExpectedRevision implements the optimistic-concurrency check used by
// direct write commands (task/run). An empty expectation means "use the lock
// only".
func CheckExpectedRevision(l *Ledger, expected string) error {
	if expected == "" {
		return nil
	}
	rev, err := l.Revision()
	if err != nil {
		return err
	}
	if rev != expected {
		return revisionConflict(expected, rev)
	}
	return nil
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func (l *Ledger) LoadRunSpec(runID string) (*schema.RunSpec, error) {
	path := filepath.Join(l.RunDir(runID), "run_spec.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Errorf(CodeNotFound, "run %s not found (expected %s)", runID, path)
		}
		return nil, Wrap(CodeGeneral, "read run_spec.json", err)
	}
	var spec schema.RunSpec
	if err := jsonUnmarshal(raw, &spec); err != nil {
		return nil, Wrap(CodeGeneral, "parse run_spec.json", err)
	}
	if err := spec.Validate(); err != nil {
		return nil, Wrap(CodeGeneral, "invalid run_spec.json", err)
	}
	return &spec, nil
}

func jsonUnmarshal(raw []byte, v any) error {
	return json.Unmarshal(raw, v)
}
