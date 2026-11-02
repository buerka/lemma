package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buerka/lemma/internal/schema"
	"gopkg.in/yaml.v3"
)

func (l *Ledger) Path(name string) string { return filepath.Join(l.Dir, name) }
func (l *Ledger) EventsPath() string      { return l.Path(eventsFile) }
func (l *Ledger) GatesPath() string       { return l.Path(gatesFile) }
func (l *Ledger) TasksPath() string       { return l.Path(tasksFile) }
func (l *Ledger) StatePath() string       { return l.Path(stateFile) }
func (l *Ledger) ReviewPath() string      { return l.Path(reviewFile) }
func (l *Ledger) RunsDir() string         { return l.Path("runs") }
func (l *Ledger) ChangesetsDir() string   { return l.Path("changesets") }
func (l *Ledger) ReceiptsDir() string     { return l.Path("receipts") }
func (l *Ledger) Git() Git                { return Git{Dir: l.Dir} }

func (l *Ledger) RunDir(runID string) string { return filepath.Join(l.RunsDir(), runID) }
func (l *Ledger) ReceiptPath(id string) string {
	return filepath.Join(l.ReceiptsDir(), id+".json")
}

// ComputeEventHash hashes an event with its hash field cleared.
func ComputeEventHash(e schema.Event) (string, error) {
	e.Hash = ""
	return HashValue(e)
}

// ReadEvents parses events.jsonl preserving JSON numbers exactly.
func (l *Ledger) ReadEvents() ([]schema.Event, error) {
	return ReadEventsFile(l.EventsPath())
}

func ReadEventsFile(path string) ([]schema.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, Wrap(CodeGeneral, "open events.jsonl", err)
	}
	defer f.Close()

	var events []schema.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		var ev schema.Event
		if err := dec.Decode(&ev); err != nil {
			return nil, Errorf(CodeGeneral, "events.jsonl line %d: invalid JSON: %v", lineNo, err)
		}
		if err := ev.Validate(); err != nil {
			return nil, Errorf(CodeGeneral, "events.jsonl line %d: %v", lineNo, err)
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return nil, Wrap(CodeGeneral, "read events.jsonl", err)
	}
	return events, nil
}

// Revision returns the last event hash, or the empty-ledger hash.
func (l *Ledger) Revision() (string, error) {
	events, err := l.ReadEvents()
	if err != nil {
		return "", err
	}
	if len(events) == 0 {
		return EmptyRevision(), nil
	}
	return events[len(events)-1].Hash, nil
}

// AppendEvent assigns the next event id, hash-links it to the previous event,
// and appends it atomically. The caller must hold the ledger lock.
func (l *Ledger) AppendEvent(typ, taskID, changesetID, runID string, payload map[string]any) (schema.Event, error) {
	events, err := l.ReadEvents()
	if err != nil {
		return schema.Event{}, err
	}
	prev := ""
	if len(events) > 0 {
		prev = events[len(events)-1].Hash
	}
	ev := schema.Event{
		EventID:     fmt.Sprintf("ev-%06d", len(events)+1),
		TS:          l.NowUTC(),
		Project:     l.Project.Project,
		Type:        typ,
		TaskID:      taskID,
		ChangesetID: changesetID,
		RunID:       runID,
		Payload:     payload,
		PrevHash:    prev,
	}
	h, err := ComputeEventHash(ev)
	if err != nil {
		return schema.Event{}, Wrap(CodeGeneral, "hash event", err)
	}
	ev.Hash = h
	line, err := marshalNoEscape(ev)
	if err != nil {
		return schema.Event{}, Wrap(CodeGeneral, "encode event", err)
	}
	if err := AtomicAppendLine(l.EventsPath(), line); err != nil {
		return schema.Event{}, Wrap(CodeGeneral, "append event", err)
	}
	return ev, nil
}

// VerifyEventChain checks canonical hashes and prev_hash links.
func VerifyEventChain(events []schema.Event) []Issue {
	var issues []Issue
	prev := ""
	for i, ev := range events {
		wantID := fmt.Sprintf("ev-%06d", i+1)
		if ev.EventID != wantID {
			issues = append(issues, Issue{Code: "EVENT_ID", Message: fmt.Sprintf("event %d has id %q, expected %q", i+1, ev.EventID, wantID)})
		}
		if ev.PrevHash != prev {
			issues = append(issues, Issue{Code: "EVENT_LINK", Message: fmt.Sprintf("event %s prev_hash %q does not match previous hash %q", ev.EventID, ev.PrevHash, prev)})
		}
		got, err := ComputeEventHash(ev)
		if err != nil {
			issues = append(issues, Issue{Code: "EVENT_HASH", Message: fmt.Sprintf("event %s: %v", ev.EventID, err)})
		} else if got != ev.Hash {
			issues = append(issues, Issue{Code: "EVENT_HASH", Message: fmt.Sprintf("event %s hash mismatch: recorded %s, computed %s", ev.EventID, ev.Hash, got)})
		}
		if _, err := time.Parse(time.RFC3339, ev.TS); err != nil {
			issues = append(issues, Issue{Code: "EVENT_TS", Message: fmt.Sprintf("event %s has invalid timestamp %q", ev.EventID, ev.TS)})
		}
		if ev.Project != "" && ev.Project != events[0].Project {
			issues = append(issues, Issue{Code: "EVENT_PROJECT", Message: fmt.Sprintf("event %s belongs to project %q, ledger is %q", ev.EventID, ev.Project, events[0].Project)})
		}
		prev = ev.Hash
	}
	return issues
}

func MarshalYAML(v any) ([]byte, error) { return marshalYAML(v) }

func marshalYAML(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, Wrap(CodeGeneral, "encode yaml", err)
	}
	if err := enc.Close(); err != nil {
		return nil, Wrap(CodeGeneral, "close yaml encoder", err)
	}
	return buf.Bytes(), nil
}

func (l *Ledger) LoadTasks() (schema.TasksFile, error) {
	raw, err := os.ReadFile(l.TasksPath())
	if err != nil {
		return schema.TasksFile{}, Wrap(CodeGeneral, "read tasks.yaml", err)
	}
	var tf schema.TasksFile
	if err := yaml.Unmarshal(raw, &tf); err != nil {
		return schema.TasksFile{}, Wrap(CodeGeneral, "parse tasks.yaml", err)
	}
	if tf.Tasks == nil {
		tf.Tasks = []schema.Task{}
	}
	if err := tf.Validate(); err != nil {
		return schema.TasksFile{}, Wrap(CodeGeneral, "invalid tasks.yaml", err)
	}
	return tf, nil
}

func (l *Ledger) SaveTasks(tf schema.TasksFile) error {
	if err := tf.Validate(); err != nil {
		return Wrap(CodeGeneral, "refusing to write invalid tasks.yaml", err)
	}
	b, err := marshalYAML(tf)
	if err != nil {
		return err
	}
	return AtomicWrite(l.TasksPath(), b, 0o644)
}

func (l *Ledger) ReadGates() ([]schema.Gate, error) {
	return ReadGatesFile(l.GatesPath())
}

func ReadGatesFile(path string) ([]schema.Gate, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, Wrap(CodeGeneral, "open gates.jsonl", err)
	}
	defer f.Close()
	var gates []schema.Gate
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var g schema.Gate
		if err := json.Unmarshal(line, &g); err != nil {
			return nil, Errorf(CodeGeneral, "gates.jsonl line %d: invalid JSON: %v", lineNo, err)
		}
		if err := g.Validate(); err != nil {
			return nil, Errorf(CodeGeneral, "gates.jsonl line %d: %v", lineNo, err)
		}
		gates = append(gates, g)
	}
	if err := sc.Err(); err != nil {
		return nil, Wrap(CodeGeneral, "read gates.jsonl", err)
	}
	return gates, nil
}

func (l *Ledger) AppendGate(g schema.Gate) (schema.Gate, error) {
	gates, err := l.ReadGates()
	if err != nil {
		return schema.Gate{}, err
	}
	g.GateID = fmt.Sprintf("gate-%04d", len(gates)+1)
	g.Project = l.Project.Project
	if g.ApprovedAt == "" {
		g.ApprovedAt = l.NowUTC()
	}
	if err := g.Validate(); err != nil {
		return schema.Gate{}, Wrap(CodeGeneral, "invalid gate", err)
	}
	line, err := marshalNoEscape(g)
	if err != nil {
		return schema.Gate{}, Wrap(CodeGeneral, "encode gate", err)
	}
	if err := AtomicAppendLine(l.GatesPath(), line); err != nil {
		return schema.Gate{}, Wrap(CodeGeneral, "append gate", err)
	}
	return g, nil
}

func (l *Ledger) LoadReceipt(id string) (*schema.Receipt, error) {
	raw, err := os.ReadFile(l.ReceiptPath(id))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, Wrap(CodeGeneral, "read receipt", err)
	}
	var r schema.Receipt
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, Wrap(CodeGeneral, "parse receipt", err)
	}
	return &r, nil
}

func (l *Ledger) SaveReceipt(r schema.Receipt) error {
	if err := r.Validate(); err != nil {
		return Wrap(CodeGeneral, "invalid receipt", err)
	}
	b, err := MarshalIndentNoEscape(r)
	if err != nil {
		return Wrap(CodeGeneral, "encode receipt", err)
	}
	return AtomicWrite(l.ReceiptPath(r.ChangesetID), b, 0o644)
}

func (l *Ledger) Commit(message string) (string, bool, error) {
	return l.Git().Commit(message)
}

func GateForHash(gates []schema.Gate, changesetID, hash string) *schema.Gate {
	for i := range gates {
		if gates[i].ChangesetID == changesetID && gates[i].ChangesetHash == hash {
			return &gates[i]
		}
	}
	return nil
}

func GateExistsForHash(gates []schema.Gate, hash string) *schema.Gate {
	for i := range gates {
		if gates[i].ChangesetHash == hash {
			return &gates[i]
		}
	}
	return nil
}

func HashString(s string) string { return hashBytes([]byte(s)) }

func TrimHashPrefix(s string) string { return strings.TrimPrefix(s, "sha256:") }
