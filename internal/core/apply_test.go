package core

import (
	"testing"
	"time"

	"github.com/buerka/lemma/internal/schema"
)

func newApplyTestLedger(t *testing.T) *Ledger {
	t.Helper()
	l, _, err := InitLedger(InitOptions{
		Root: t.TempDir(),
		Project: schema.Project{
			Project: "test", SchemaVersion: schema.SchemaVersion,
			Remote: schema.RemoteConfig{Transport: "ssh-mcp", Host: "gpu", Workdir: "~/p", RunRoot: "~/.lem-runs", Python: "python3"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	tasks := schema.TasksFile{Tasks: []schema.Task{{ID: "T1", Title: "task", Owner: "agent", Status: "todo"}}}
	if err := l.SaveTasks(tasks); err != nil {
		t.Fatal(err)
	}
	if _, err := l.AppendEvent("task.add", "T1", "", "", map[string]any{"id": "T1", "title": "task"}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.WriteState(true); err != nil {
		t.Fatal(err)
	}
	if _, _, err := l.Commit("task: add T1"); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestApplyConflictAndIdempotency(t *testing.T) {
	l := newApplyTestLedger(t)
	timeout := 5 * time.Second

	csA, _, err := Plan(l, PlanOptions{TaskID: "T1", Status: "doing", Out: l.ChangesetPath("cs-a"), LockTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	csB, _, err := Plan(l, PlanOptions{TaskID: "T1", Status: "blocked", Out: l.ChangesetPath("cs-b"), LockTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Approve(l, csA.ChangesetID, ApproveOptions{By: "human", LockTimeout: timeout}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Approve(l, csB.ChangesetID, ApproveOptions{By: "human", LockTimeout: timeout}); err != nil {
		t.Fatal(err)
	}

	res, err := Apply(l, csA.ChangesetID, ApplyOptions{LockTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	if res.AlreadyApplied {
		t.Fatal("first apply reported idempotent")
	}
	if res.Receipt.GitCommit == "" || res.Receipt.Revision == "" {
		t.Fatalf("incomplete receipt: %+v", res.Receipt)
	}
	tasks, err := l.LoadTasks()
	if err != nil {
		t.Fatal(err)
	}
	if got := tasks.Find("T1").Status; got != "doing" {
		t.Fatalf("task status = %s, want doing", got)
	}

	again, err := Apply(l, csA.ChangesetID, ApplyOptions{LockTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	if !again.AlreadyApplied || again.Receipt.ChangesetHash != res.Receipt.ChangesetHash {
		t.Fatal("second apply was not idempotent")
	}

	_, err = Apply(l, csB.ChangesetID, ApplyOptions{LockTimeout: timeout})
	if CodeOf(err) != CodeConflict {
		t.Fatalf("stale apply error = %v, want CONFLICT", err)
	}

	report, err := Verify(l, timeout)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("verify issues: %+v", report.Issues)
	}
}

func TestApplyRequiresGate(t *testing.T) {
	l := newApplyTestLedger(t)
	timeout := 5 * time.Second
	cs, _, err := Plan(l, PlanOptions{TaskID: "T1", Status: "doing", Out: l.ChangesetPath("cs-gate"), LockTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	_, err = Apply(l, cs.ChangesetID, ApplyOptions{LockTimeout: timeout})
	if err == nil {
		t.Fatal("apply without gate succeeded")
	}
	if CodeOf(err) != CodeGeneral {
		t.Fatalf("error code = %s, want GENERAL", CodeOf(err))
	}
}

func TestApplyRollbackOnInjectedFailure(t *testing.T) {
	l := newApplyTestLedger(t)
	timeout := 5 * time.Second
	cs, _, err := Plan(l, PlanOptions{TaskID: "T1", Status: "doing", Out: l.ChangesetPath("cs-crash"), LockTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := Approve(l, cs.ChangesetID, ApproveOptions{By: "human", LockTimeout: timeout}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEM_FAILPOINT", "apply-after-task-write")
	if _, err := Apply(l, cs.ChangesetID, ApplyOptions{LockTimeout: timeout}); err == nil {
		t.Fatal("injected failure did not fail apply")
	}
	t.Setenv("LEM_FAILPOINT", "")
	report, err := Verify(l, timeout)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("verify after rollback: %+v", report.Issues)
	}
	tasks, err := l.LoadTasks()
	if err != nil {
		t.Fatal(err)
	}
	if got := tasks.Find("T1").Status; got != "todo" {
		t.Fatalf("task status after rollback = %s, want todo", got)
	}
	if _, err := Apply(l, cs.ChangesetID, ApplyOptions{LockTimeout: timeout}); err != nil {
		t.Fatalf("recovery apply failed: %v", err)
	}
}
