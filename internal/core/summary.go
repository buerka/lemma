package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/buerka/lemma/internal/schema"
	tmpl "github.com/buerka/lemma/internal/templates"
)

type PendingChangeset struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	Hash     string `json:"hash"`
	Approved bool   `json:"approved"`
}

type Summary struct {
	Project           string             `json:"project"`
	Root              string             `json:"root"`
	Ledger            string             `json:"ledger"`
	Revision          string             `json:"revision"`
	SchemaVersion     int                `json:"schema_version"`
	Phase             string             `json:"phase"`
	Counts            map[string]int     `json:"counts"`
	Tasks             []schema.Task      `json:"tasks"`
	Active            []schema.Task      `json:"active_tasks"`
	Blocked           []schema.Task      `json:"blocked_tasks"`
	Next              *schema.Task       `json:"next_task"`
	PendingChangesets []PendingChangeset `json:"pending_changesets"`
	GateCount         int                `json:"gate_count"`
	ReceiptCount      int                `json:"receipt_count"`
	RecentEvents      []schema.Event     `json:"recent_events"`
	GitClean          bool               `json:"git_clean"`
}

func BuildSummary(l *Ledger) (*Summary, error) {
	tasks, err := l.LoadTasks()
	if err != nil {
		return nil, err
	}
	events, err := l.ReadEvents()
	if err != nil {
		return nil, err
	}
	gates, err := l.ReadGates()
	if err != nil {
		return nil, err
	}
	infos, err := ListChangesets(l)
	if err != nil {
		return nil, err
	}
	receipts, err := countFiles(l.ReceiptsDir(), ".json")
	if err != nil {
		return nil, err
	}

	rev := EmptyRevision()
	if len(events) > 0 {
		rev = events[len(events)-1].Hash
	}
	s := &Summary{
		Project:       l.Project.Project,
		Root:          l.Root,
		Ledger:        l.Dir,
		Revision:      rev,
		SchemaVersion: l.Project.SchemaVersion,
		Counts: map[string]int{
			schema.StatusTodo:    0,
			schema.StatusDoing:   0,
			schema.StatusBlocked: 0,
			schema.StatusDone:    0,
		},
		Tasks:             tasks.Tasks,
		Active:            []schema.Task{},
		Blocked:           []schema.Task{},
		PendingChangesets: []PendingChangeset{},
		RecentEvents:      []schema.Event{},
		GateCount:         len(gates),
		ReceiptCount:      receipts,
	}
	for _, t := range tasks.Tasks {
		s.Counts[t.Status]++
		switch t.Status {
		case schema.StatusDoing:
			s.Active = append(s.Active, t)
		case schema.StatusBlocked:
			s.Blocked = append(s.Blocked, t)
		}
	}
	s.Next = NextTask(tasks)
	s.Phase = CurrentPhase(tasks)
	for _, info := range infos {
		if info.Applied {
			continue
		}
		s.PendingChangesets = append(s.PendingChangesets, PendingChangeset{
			ID:       info.Changeset.ChangesetID,
			Path:     info.Path,
			Hash:     info.Changeset.Hash,
			Approved: info.GateExists,
		})
	}
	for i := len(events) - 1; i >= 0 && len(s.RecentEvents) < 10; i-- {
		s.RecentEvents = append(s.RecentEvents, events[i])
	}
	clean, err := l.Git().IsClean()
	if err == nil {
		s.GitClean = clean
	}
	return s, nil
}

func countFiles(dir, suffix string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, Wrap(CodeGeneral, "read directory", err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), suffix) {
			n++
		}
	}
	return n, nil
}

// NextTask picks the first todo task whose dependencies are done, ordered by
// phase and then declaration order.
func NextTask(tf schema.TasksFile) *schema.Task {
	done := map[string]bool{}
	for _, t := range tf.Tasks {
		if t.Status == schema.StatusDone {
			done[t.ID] = true
		}
	}
	idxs := make([]int, len(tf.Tasks))
	for i := range tf.Tasks {
		idxs[i] = i
	}
	sort.SliceStable(idxs, func(a, b int) bool {
		ta, tb := tf.Tasks[idxs[a]], tf.Tasks[idxs[b]]
		pa, pb := ta.Phase, tb.Phase
		if pa == "" {
			pa = "\uffff"
		}
		if pb == "" {
			pb = "\uffff"
		}
		return pa < pb
	})
	for _, i := range idxs {
		t := tf.Tasks[i]
		if t.Status != schema.StatusTodo {
			continue
		}
		ready := true
		for _, dep := range t.DependsOn {
			if !done[dep] {
				ready = false
				break
			}
		}
		if ready {
			cp := t
			return &cp
		}
	}
	return nil
}

// CurrentPhase returns the first phase with unfinished work.
func CurrentPhase(tf schema.TasksFile) string {
	if len(tf.Tasks) == 0 {
		return "none"
	}
	phases := map[string]bool{}
	any := false
	for _, t := range tf.Tasks {
		if t.Status != schema.StatusDone {
			any = true
			p := t.Phase
			if p == "" {
				p = "(unphased)"
			}
			phases[p] = true
		}
	}
	if !any {
		return "done"
	}
	list := make([]string, 0, len(phases))
	for p := range phases {
		list = append(list, p)
	}
	sort.Strings(list)
	return list[0]
}

// WriteState regenerates STATE.md. When force is false and the existing file
// already has the current revision marker, nothing is written.
func (l *Ledger) WriteState(force bool) (bool, error) {
	summary, err := BuildSummary(l)
	if err != nil {
		return false, err
	}
	data := tmpl.StateData{
		Project:           summary.Project,
		Revision:          summary.Revision,
		Phase:             summary.Phase,
		CountTodo:         summary.Counts[schema.StatusTodo],
		CountDoing:        summary.Counts[schema.StatusDoing],
		CountBlocked:      summary.Counts[schema.StatusBlocked],
		CountDone:         summary.Counts[schema.StatusDone],
		CountTotal:        len(summary.Tasks),
		PendingChangesets: []string{},
	}
	for _, t := range summary.Tasks {
		data.Tasks = append(data.Tasks, tmpl.TaskRow{
			ID:         t.ID,
			Phase:      t.Phase,
			Title:      t.Title,
			Owner:      t.Owner,
			Status:     t.Status,
			DependsOn:  strings.Join(t.DependsOn, ", "),
			Acceptance: t.Acceptance,
		})
	}
	if summary.Next != nil {
		data.NextTask = &tmpl.TaskRow{
			ID:         summary.Next.ID,
			Phase:      summary.Next.Phase,
			Title:      summary.Next.Title,
			Owner:      summary.Next.Owner,
			Status:     summary.Next.Status,
			DependsOn:  strings.Join(summary.Next.DependsOn, ", "),
			Acceptance: summary.Next.Acceptance,
		}
	}
	for _, p := range summary.PendingChangesets {
		label := p.ID
		if p.Approved {
			label += " (approved)"
		} else {
			label += " (awaiting approval)"
		}
		data.PendingChangesets = append(data.PendingChangesets, label)
	}
	for _, ev := range summary.RecentEvents {
		data.RecentEvents = append(data.RecentEvents, tmpl.EventRow{
			TS:      ev.TS,
			Type:    ev.Type,
			TaskID:  ev.TaskID,
			Summary: EventSummary(ev),
		})
	}
	fp, err := HashValue(data)
	if err != nil {
		return false, Wrap(CodeGeneral, "fingerprint STATE.md", err)
	}
	data.Fingerprint = fp
	marker := "<!-- lem:revision=" + summary.Revision + " state=" + fp + " -->"
	if !force {
		if old, err := os.ReadFile(l.StatePath()); err == nil && strings.HasPrefix(string(old), marker) {
			return false, nil
		}
	}
	data.GeneratedAt = l.NowUTC()
	b, err := tmpl.RenderState(data)
	if err != nil {
		return false, Wrap(CodeGeneral, "render STATE.md", err)
	}
	if err := AtomicWrite(l.StatePath(), b, 0o644); err != nil {
		return false, Wrap(CodeGeneral, "write STATE.md", err)
	}
	return true, nil
}

func EventSummary(ev schema.Event) string {
	val := func(k string) string {
		v, ok := ev.Payload[k]
		if !ok {
			return ""
		}
		switch x := v.(type) {
		case string:
			return x
		case json.Number:
			return x.String()
		case float64:
			return fmt.Sprintf("%v", x)
		case bool:
			return fmt.Sprintf("%v", x)
		default:
			b, _ := json.Marshal(x)
			return string(b)
		}
	}
	switch ev.Type {
	case "project.init":
		return "ledger initialized"
	case "task.add":
		if title := val("title"); title != "" {
			return "added task " + val("id") + ": " + title
		}
		return "added task " + ev.TaskID
	case "task.update":
		from, to := val("from"), val("to")
		if from != "" || to != "" {
			return fmt.Sprintf("task %s status %s -> %s", ev.TaskID, from, to)
		}
		return "updated task " + ev.TaskID
	case "run.start":
		return "started run " + ev.RunID
	case "run.finish":
		s := "finished run " + ev.RunID
		if st := val("status"); st != "" {
			s += ": " + st
		}
		return s
	case "changeset.apply":
		if intent := val("intent"); intent != "" {
			return "applied " + ev.ChangesetID + ": " + intent
		}
		return "applied " + ev.ChangesetID
	case "changeset.plan":
		return "planned " + ev.ChangesetID
	default:
		if len(ev.Payload) == 0 {
			return ev.Type
		}
		b, err := marshalNoEscape(ev.Payload)
		if err != nil {
			return ev.Type
		}
		s := string(b)
		if len(s) > 80 {
			s = s[:77] + "..."
		}
		return s
	}
}

// FormatClock is a small helper for tests and human output.
func FormatClock(t time.Time) string { return t.Format(time.RFC3339) }

func (l *Ledger) Rel(path string) string {
	rel, err := filepath.Rel(l.Root, path)
	if err != nil {
		return path
	}
	return rel
}
