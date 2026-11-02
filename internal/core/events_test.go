package core

import (
	"testing"

	"github.com/buerka/lemma/internal/schema"
)

func TestEventChainAppendAndVerify(t *testing.T) {
	l, genesis, err := InitLedger(InitOptions{
		Root:      t.TempDir(),
		Project:   schema.Project{Project: "test", SchemaVersion: schema.SchemaVersion},
		NoGitInit: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	ev2, err := l.AppendEvent("task.add", "T1", "", "", map[string]any{"id": "T1", "title": "x"})
	if err != nil {
		t.Fatal(err)
	}
	ev3, err := l.AppendEvent("task.update", "T1", "", "", map[string]any{"from": "todo", "to": "doing"})
	if err != nil {
		t.Fatal(err)
	}
	if ev2.PrevHash != genesis.Hash || ev3.PrevHash != ev2.Hash {
		t.Fatalf("prev_hash links are wrong: %s %s %s", genesis.Hash, ev2.PrevHash, ev3.PrevHash)
	}
	events, err := l.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("expected 3 events, got %d", len(events))
	}
	if issues := VerifyEventChain(events); len(issues) != 0 {
		t.Fatalf("unexpected issues: %+v", issues)
	}
	rev, err := l.Revision()
	if err != nil {
		t.Fatal(err)
	}
	if rev != ev3.Hash {
		t.Fatalf("revision %s != last hash %s", rev, ev3.Hash)
	}

	events[1].Hash = events[2].Hash
	if issues := VerifyEventChain(events); len(issues) == 0 {
		t.Fatal("tampered chain was accepted")
	}
}
