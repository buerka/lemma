package core

import (
	"os"
	"path/filepath"
	"time"

	"github.com/buerka/lemma/internal/schema"
)

// Apply atomically applies an approved changeset. The success path is:
//
//	lock -> re-read state -> checks -> write tasks -> append event ->
//	git commit state -> write receipt -> git commit receipt -> unlock
func Apply(l *Ledger, ref string, opts ApplyOptions) (*ApplyResult, error) {
	lock, err := AcquireLock(l.Dir, opts.LockTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()

	path, cs, err := FindChangeset(l.Dir, ref)
	if err != nil {
		return nil, err
	}
	if !WithinDir(l.ChangesetsDir(), path) {
		return nil, Errorf(CodeUsage, "changeset must live under %s", l.ChangesetsDir())
	}
	if cs.Project != l.Project.Project {
		return nil, Errorf(CodeConflict, "changeset belongs to project %q, ledger is %q", cs.Project, l.Project.Project)
	}
	got, err := ComputeChangesetHash(*cs)
	if err != nil {
		return nil, Wrap(CodeGeneral, "hash changeset", err)
	}
	if got != cs.Hash {
		return nil, Errorf(CodeConflict, "changeset hash mismatch: recorded %s, computed %s; re-plan", cs.Hash, got)
	}

	receipt, err := l.LoadReceipt(cs.ChangesetID)
	if err != nil {
		return nil, err
	}
	if receipt != nil {
		if receipt.ChangesetHash != cs.Hash {
			return nil, Errorf(CodeConflict, "changeset %s already has a receipt for a different hash", cs.ChangesetID)
		}
		return &ApplyResult{Receipt: *receipt, Changeset: *cs, Path: path, AlreadyApplied: true}, nil
	}

	rev, err := l.Revision()
	if err != nil {
		return nil, err
	}
	if opts.ExpectedRevision != "" && opts.ExpectedRevision != rev {
		return nil, revisionConflict(opts.ExpectedRevision, rev)
	}
	if cs.BaseRevision != rev {
		return nil, revisionConflict(cs.BaseRevision, rev)
	}

	gates, err := l.ReadGates()
	if err != nil {
		return nil, err
	}
	if GateForHash(gates, cs.ChangesetID, cs.Hash) == nil {
		return nil, Errorf(CodeGeneral, "changeset %s has no approval gate for hash %s; run lem approve first", cs.ChangesetID, cs.Hash)
	}

	clean, err := l.Git().IsClean()
	if err != nil {
		return nil, Wrap(CodeGeneral, "check ledger git status", err)
	}
	if !clean {
		return nil, Errorf(CodeGeneral, "progress/ has uncommitted changes; commit or discard them before applying")
	}
	prevCommit := l.Git().HeadOrEmpty()

	tasks, err := l.LoadTasks()
	if err != nil {
		return nil, err
	}
	tasksSnap, tasksExisted := readSnapshot(l.TasksPath())
	eventsSnap, eventsExisted := readSnapshot(l.EventsPath())
	stateSnap, stateExisted := readSnapshot(l.StatePath())

	applied := false
	defer func() {
		if applied {
			return
		}
		restoreSnapshot(l.TasksPath(), tasksSnap, tasksExisted)
		restoreSnapshot(l.EventsPath(), eventsSnap, eventsExisted)
		restoreSnapshot(l.StatePath(), stateSnap, stateExisted)
		l.Git().ResetIndex()
	}()

	now := l.NowUTC()
	for _, op := range cs.Operations {
		switch op.Op {
		case "task.update":
			t := tasks.Find(op.ID)
			if t == nil {
				return nil, Errorf(CodeNotFound, "task %s not found", op.ID)
			}
			if t.Status != op.From {
				return nil, Errorf(CodeConflict, "task %s is %q, changeset expects %q; re-plan", op.ID, t.Status, op.From)
			}
			t.Status = op.To
			t.UpdatedAt = now
		case "run.append":
			if _, err := l.LoadRunSpec(op.RunID); err != nil {
				return nil, err
			}
		default:
			return nil, Errorf(CodeGeneral, "unsupported operation %q", op.Op)
		}
	}
	if err := l.SaveTasks(tasks); err != nil {
		return nil, err
	}
	if failpoint() == "apply-after-task-write" {
		return nil, Errorf(CodeGeneral, "injected failure at apply-after-task-write")
	}

	ev, err := l.AppendEvent("changeset.apply", cs.TaskID, cs.ChangesetID, "", map[string]any{
		"changeset_hash": cs.Hash,
		"base_revision":  cs.BaseRevision,
		"intent":         cs.Intent,
		"operations":     cs.Operations,
		"evidence":       cs.Evidence,
	})
	if err != nil {
		return nil, err
	}
	if failpoint() == "apply-after-event" {
		return nil, Errorf(CodeGeneral, "injected failure at apply-after-event")
	}
	if _, err := l.WriteState(true); err != nil {
		return nil, err
	}
	dataCommit, _, err := l.Commit("apply: " + cs.ChangesetID + " " + cs.Intent)
	if err != nil {
		return nil, Wrap(CodeGeneral, "commit applied state", err)
	}
	applied = true // state is committed; never roll back below this point

	rec := schema.Receipt{
		ChangesetID:   cs.ChangesetID,
		Project:       l.Project.Project,
		ChangesetHash: cs.Hash,
		AppliedAt:     now,
		GitCommit:     dataCommit,
		PrevCommit:    prevCommit,
		Revision:      ev.Hash,
		Status:        "applied",
	}
	if err := l.SaveReceipt(rec); err != nil {
		return nil, err
	}
	var commitErr error
	for attempt := 0; attempt < 3; attempt++ {
		if _, _, commitErr = l.Commit("receipt: " + cs.ChangesetID); commitErr == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if commitErr != nil {
		return nil, Wrap(CodeGeneral, "state applied but receipt commit failed; run lem verify", commitErr)
	}
	return &ApplyResult{Receipt: rec, Changeset: *cs, Path: path}, nil
}

func failpoint() string {
	return os.Getenv("LEM_FAILPOINT")
}

func readSnapshot(path string) ([]byte, bool) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, false
	}
	return b, true
}

func restoreSnapshot(path string, data []byte, existed bool) {
	if existed {
		_ = AtomicWrite(path, data, 0o644)
		return
	}
	_ = os.Remove(path)
}

func (l *Ledger) ChangesetPath(id string) string {
	return filepath.Join(l.ChangesetsDir(), id+".json")
}
