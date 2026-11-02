package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buerka/lemma/internal/schema"
)

type Issue struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Path    string `json:"path,omitempty"`
}

type VerifyReport struct {
	OK             bool    `json:"ok"`
	Project        string  `json:"project"`
	Revision       string  `json:"revision"`
	Head           string  `json:"head"`
	GitClean       bool    `json:"git_clean"`
	EventCount     int     `json:"event_count"`
	ChangesetCount int     `json:"changeset_count"`
	ReceiptCount   int     `json:"receipt_count"`
	GateCount      int     `json:"gate_count"`
	Issues         []Issue `json:"issues"`
}

// Verify checks schema, the event hash chain, gates, receipts, and that the
// progress repository is clean. The caller should not hold the ledger lock.
func Verify(l *Ledger, lockTimeout time.Duration) (*VerifyReport, error) {
	lock, err := AcquireLock(l.Dir, lockTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()

	report := &VerifyReport{Project: l.Project.Project, OK: true}

	tasks, err := l.LoadTasks()
	if err != nil {
		report.Issues = append(report.Issues, Issue{Code: "TASKS", Message: err.Error(), Path: l.TasksPath()})
	}
	_ = tasks

	events, eventErr := l.ReadEvents()
	if eventErr != nil {
		report.Issues = append(report.Issues, Issue{Code: "EVENTS", Message: eventErr.Error(), Path: l.EventsPath()})
	} else {
		report.EventCount = len(events)
		report.Issues = append(report.Issues, VerifyEventChain(events)...)
		for _, ev := range events {
			if ev.Project != l.Project.Project {
				report.Issues = append(report.Issues, Issue{Code: "EVENT_PROJECT", Message: fmt.Sprintf("event %s belongs to %q, ledger is %q", ev.EventID, ev.Project, l.Project.Project)})
				break
			}
		}
		if len(events) == 0 {
			report.Issues = append(report.Issues, Issue{Code: "EVENTS", Message: "ledger has no genesis event"})
			report.Revision = EmptyRevision()
		} else {
			report.Revision = events[len(events)-1].Hash
		}
	}
	revisions := map[string]bool{"": true, EmptyRevision(): true}
	for _, ev := range events {
		revisions[ev.Hash] = true
	}

	infoByID := map[string]ChangesetInfo{}
	infos, err := ListChangesets(l)
	if err != nil {
		report.Issues = append(report.Issues, Issue{Code: "CHANGESETS", Message: err.Error(), Path: l.ChangesetsDir()})
	}
	entries, _ := os.ReadDir(l.ChangesetsDir())
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(l.ChangesetsDir(), e.Name())
		cs, valid, err := VerifyChangesetFile(p)
		if err != nil {
			report.Issues = append(report.Issues, Issue{Code: "CHANGESET", Message: err.Error(), Path: p})
			continue
		}
		report.ChangesetCount++
		if !valid {
			report.Issues = append(report.Issues, Issue{Code: "CHANGESET_HASH", Message: fmt.Sprintf("changeset %s hash does not match its contents", cs.ChangesetID), Path: p})
		}
		if cs.Project != l.Project.Project {
			report.Issues = append(report.Issues, Issue{Code: "CHANGESET_PROJECT", Message: fmt.Sprintf("changeset %s belongs to %q", cs.ChangesetID, cs.Project), Path: p})
		}
		if !revisions[cs.BaseRevision] {
			report.Issues = append(report.Issues, Issue{Code: "CHANGESET_BASE", Message: fmt.Sprintf("changeset %s base_revision %s is not in the event chain", cs.ChangesetID, cs.BaseRevision), Path: p})
		}
	}
	for _, info := range infos {
		if info.HashValid {
			infoByID[info.Changeset.ChangesetID] = info
		}
	}

	gates, gateErr := l.ReadGates()
	if gateErr != nil {
		report.Issues = append(report.Issues, Issue{Code: "GATES", Message: gateErr.Error(), Path: l.GatesPath()})
	} else {
		report.GateCount = len(gates)
		for _, g := range gates {
			info, ok := infoByID[g.ChangesetID]
			if !ok {
				report.Issues = append(report.Issues, Issue{Code: "GATE_CHANGESET", Message: fmt.Sprintf("gate %s references missing changeset %s", g.GateID, g.ChangesetID), Path: l.GatesPath()})
				continue
			}
			if info.Changeset.Hash != g.ChangesetHash {
				report.Issues = append(report.Issues, Issue{Code: "GATE_HASH", Message: fmt.Sprintf("gate %s approves hash %s but changeset %s is %s", g.GateID, g.ChangesetHash, g.ChangesetID, info.Changeset.Hash), Path: l.GatesPath()})
			}
		}
	}
	gateByHash := map[string]bool{}
	for _, g := range gates {
		gateByHash[g.ChangesetHash] = true
	}

	receiptEntries, _ := os.ReadDir(l.ReceiptsDir())
	for _, e := range receiptEntries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		p := filepath.Join(l.ReceiptsDir(), e.Name())
		raw, err := os.ReadFile(p)
		if err != nil {
			report.Issues = append(report.Issues, Issue{Code: "RECEIPT", Message: err.Error(), Path: p})
			continue
		}
		var r schema.Receipt
		if err := json.Unmarshal(raw, &r); err != nil {
			report.Issues = append(report.Issues, Issue{Code: "RECEIPT", Message: "invalid JSON: " + err.Error(), Path: p})
			continue
		}
		report.ReceiptCount++
		if err := r.Validate(); err != nil {
			report.Issues = append(report.Issues, Issue{Code: "RECEIPT", Message: err.Error(), Path: p})
			continue
		}
		if r.ChangesetID+".json" != e.Name() {
			report.Issues = append(report.Issues, Issue{Code: "RECEIPT_NAME", Message: fmt.Sprintf("receipt file name %s does not match changeset %s", e.Name(), r.ChangesetID), Path: p})
		}
		info, ok := infoByID[r.ChangesetID]
		if !ok {
			report.Issues = append(report.Issues, Issue{Code: "RECEIPT_CHANGESET", Message: "receipt references a missing or unhashed changeset " + r.ChangesetID, Path: p})
		} else if info.Changeset.Hash != r.ChangesetHash {
			report.Issues = append(report.Issues, Issue{Code: "RECEIPT_HASH", Message: fmt.Sprintf("receipt hash %s does not match changeset %s", r.ChangesetHash, r.ChangesetID), Path: p})
		}
		if !gateByHash[r.ChangesetHash] {
			report.Issues = append(report.Issues, Issue{Code: "RECEIPT_GATE", Message: "no gate exists for receipt hash " + r.ChangesetHash, Path: p})
		}
		if !revisions[r.Revision] {
			report.Issues = append(report.Issues, Issue{Code: "RECEIPT_REVISION", Message: "receipt revision is not in the event chain: " + r.Revision, Path: p})
		}
		if !l.Git().CommitExists(r.GitCommit) {
			report.Issues = append(report.Issues, Issue{Code: "RECEIPT_COMMIT", Message: "receipt git_commit does not exist: " + r.GitCommit, Path: p})
		}
	}

	report.Head = l.Git().HeadOrEmpty()
	if report.Head == "" {
		report.Issues = append(report.Issues, Issue{Code: "GIT", Message: "progress/ has no git commit", Path: l.Dir})
	}
	clean, err := l.Git().IsClean()
	if err != nil {
		report.Issues = append(report.Issues, Issue{Code: "GIT", Message: err.Error(), Path: l.Dir})
	} else {
		report.GitClean = clean
		if !clean {
			dirty, _ := l.Git().Dirty()
			for _, line := range dirty {
				report.Issues = append(report.Issues, Issue{Code: "GIT_DIRTY", Message: "uncommitted change: " + line, Path: l.Dir})
			}
		}
	}
	if temps := findTemps(l.Dir); len(temps) > 0 {
		for _, t := range temps {
			report.Issues = append(report.Issues, Issue{Code: "TEMP_FILE", Message: "stale temporary file (crash?)", Path: t})
		}
	}

	report.OK = len(report.Issues) == 0
	return report, nil
}

func findTemps(dir string) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasPrefix(d.Name(), ".tmp-") {
			out = append(out, path)
		}
		return nil
	})
	return out
}
