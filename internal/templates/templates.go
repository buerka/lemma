// Package templates provides typed renderers for Lemma's embedded Markdown.
package templates

import (
	"fmt"

	root "github.com/buerka/lemma/templates"
)

type StateData struct {
	Project           string
	Revision          string
	Fingerprint       string
	GeneratedAt       string
	Phase             string
	CountTodo         int
	CountDoing        int
	CountBlocked      int
	CountDone         int
	CountTotal        int
	Tasks             []TaskRow
	NextTask          *TaskRow
	PendingChangesets []string
	RecentEvents      []EventRow
}

type TaskRow struct {
	ID         string
	Phase      string
	Title      string
	Owner      string
	Status     string
	DependsOn  string
	Acceptance []string
}

type EventRow struct {
	TS      string
	Type    string
	TaskID  string
	Summary string
}

type ReviewData struct {
	ChangesetID     string
	Project         string
	Intent          string
	BaseRevision    string
	CurrentRevision string
	Hash            string
	HashValid       bool
	Stale           bool
	ChangesetPath   string
	Operations      []ReviewOperation
	Evidence        []ReviewEvidence
	Risks           []string
	Rollback        string
}

type ReviewOperation struct {
	Op     string
	Target string
	From   string
	To     string
}

type ReviewEvidence struct {
	Path   string
	Hash   string
	Status string
}

type RemoteTaskData struct {
	RunID       string
	Project     string
	TaskID      string
	Host        string
	RemoteDir   string
	LocalRunDir string
	LocalSpec   string
	Python      string
	Command     string
	CreatedAt   string
	Inputs      []RemoteInput
	Outputs     []string
}

type RemoteInput struct {
	Path   string
	SHA256 string
}

func RenderState(d StateData) ([]byte, error) {
	return root.Render("STATE.md.tmpl", d)
}

func RenderReview(d ReviewData) ([]byte, error) {
	return root.Render("REVIEW.md.tmpl", d)
}

func RenderRemoteTask(d RemoteTaskData) ([]byte, error) {
	return root.Render("REMOTE_TASK.md.tmpl", d)
}

func RunnerShell() ([]byte, error) {
	return root.Raw("runner.sh.tmpl")
}

func RunnerPython() ([]byte, error) {
	return root.Raw("runner.py.tmpl")
}

func MustRenderState(d StateData) []byte {
	b, err := RenderState(d)
	if err != nil {
		panic(fmt.Sprintf("internal template error: %v", err))
	}
	return b
}
