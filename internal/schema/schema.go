// Package schema contains the on-disk data model for Lemma.
//
// The types in this package intentionally mirror the JSON/YAML documents that
// live under progress/. They are the contract between the CLI, the ledger
// writer, and external agents.
package schema

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const (
	// SchemaVersion is the only ledger format this build understands.
	SchemaVersion = 1

	StatusTodo    = "todo"
	StatusDoing   = "doing"
	StatusBlocked = "blocked"
	StatusDone    = "done"

	OwnerAgent = "agent"
	OwnerHuman = "human"

	RunSucceeded = "succeeded"
	RunFailed    = "failed"
	RunCancelled = "cancelled"
)

var (
	taskIDRe    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	runIDRe     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	changesetRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	hashRe      = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

// Project is progress/project.yaml.
type Project struct {
	Project       string        `yaml:"project" json:"project"`
	SchemaVersion int           `yaml:"schema_version" json:"schema_version"`
	Remote        RemoteConfig  `yaml:"remote" json:"remote"`
	Storage       StorageConfig `yaml:"storage,omitempty" json:"storage,omitempty"`
}

type RemoteConfig struct {
	Transport string `yaml:"transport,omitempty" json:"transport,omitempty"`
	Host      string `yaml:"host,omitempty" json:"host,omitempty"`
	Workdir   string `yaml:"workdir,omitempty" json:"workdir,omitempty"`
	RunRoot   string `yaml:"run_root,omitempty" json:"run_root,omitempty"`
	Python    string `yaml:"python,omitempty" json:"python,omitempty"`
}

type StorageConfig struct {
	ArtifactRoot string `yaml:"artifact_root,omitempty" json:"artifact_root,omitempty"`
}

func (p Project) Validate() error {
	if strings.TrimSpace(p.Project) == "" {
		return errors.New("project name is required")
	}
	if p.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema version %d is not supported (expected %d)", p.SchemaVersion, SchemaVersion)
	}
	if p.Remote.Transport != "" && p.Remote.Transport != "ssh-mcp" {
		return fmt.Errorf("remote.transport %q is not supported (expected ssh-mcp)", p.Remote.Transport)
	}
	return nil
}

// TasksFile is progress/tasks.yaml.
type TasksFile struct {
	Tasks []Task `yaml:"tasks" json:"tasks"`
}

type Task struct {
	ID         string   `yaml:"id" json:"id"`
	Phase      string   `yaml:"phase,omitempty" json:"phase,omitempty"`
	Title      string   `yaml:"title" json:"title"`
	Owner      string   `yaml:"owner,omitempty" json:"owner,omitempty"`
	Status     string   `yaml:"status" json:"status"`
	DependsOn  []string `yaml:"depends_on,omitempty" json:"depends_on,omitempty"`
	Acceptance []string `yaml:"acceptance,omitempty" json:"acceptance,omitempty"`
	Artifacts  []string `yaml:"artifacts,omitempty" json:"artifacts,omitempty"`
	UpdatedAt  string   `yaml:"updated_at,omitempty" json:"updated_at,omitempty"`
}

func ValidTaskStatus(s string) bool {
	switch s {
	case StatusTodo, StatusDoing, StatusBlocked, StatusDone:
		return true
	}
	return false
}

func ValidOwner(s string) bool {
	return s == OwnerAgent || s == OwnerHuman
}

func (tf TasksFile) Validate() error {
	seen := map[string]bool{}
	for i, t := range tf.Tasks {
		if !taskIDRe.MatchString(t.ID) {
			return fmt.Errorf("task %d: invalid id %q", i, t.ID)
		}
		if seen[t.ID] {
			return fmt.Errorf("duplicate task id %q", t.ID)
		}
		seen[t.ID] = true
		if strings.TrimSpace(t.Title) == "" {
			return fmt.Errorf("task %s: title is required", t.ID)
		}
		if t.Status == "" {
			return fmt.Errorf("task %s: status is required", t.ID)
		}
		if !ValidTaskStatus(t.Status) {
			return fmt.Errorf("task %s: invalid status %q", t.ID, t.Status)
		}
		if t.Owner != "" && !ValidOwner(t.Owner) {
			return fmt.Errorf("task %s: invalid owner %q", t.ID, t.Owner)
		}
	}
	for _, t := range tf.Tasks {
		for _, dep := range t.DependsOn {
			if !seen[dep] {
				return fmt.Errorf("task %s: dependency %q does not exist", t.ID, dep)
			}
		}
	}
	return nil
}

func (tf *TasksFile) Find(id string) *Task {
	for i := range tf.Tasks {
		if tf.Tasks[i].ID == id {
			return &tf.Tasks[i]
		}
	}
	return nil
}

func ValidRunID(s string) bool { return runIDRe.MatchString(s) }
func ValidHash(s string) bool  { return hashRe.MatchString(s) }
func ValidChangesetID(s string) bool {
	return changesetRe.MatchString(s)
}

// Event is one line in progress/events.jsonl.
type Event struct {
	EventID     string         `json:"event_id"`
	TS          string         `json:"ts"`
	Project     string         `json:"project"`
	Type        string         `json:"type"`
	TaskID      string         `json:"task_id,omitempty"`
	ChangesetID string         `json:"changeset_id,omitempty"`
	RunID       string         `json:"run_id,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	PrevHash    string         `json:"prev_hash"`
	Hash        string         `json:"hash,omitempty"`
}

func (e Event) Validate() error {
	if e.EventID == "" {
		return errors.New("event_id is required")
	}
	if e.Type == "" {
		return errors.New("event type is required")
	}
	if e.Project == "" {
		return errors.New("event project is required")
	}
	if e.TS == "" {
		return errors.New("event ts is required")
	}
	return nil
}

// Changeset is the unit a human reviews and approves.
type Changeset struct {
	ChangesetID  string      `json:"changeset_id"`
	Project      string      `json:"project"`
	TaskID       string      `json:"task_id,omitempty"`
	Intent       string      `json:"intent"`
	BaseRevision string      `json:"base_revision"`
	Operations   []Operation `json:"operations"`
	Evidence     []string    `json:"evidence,omitempty"`
	Risks        []string    `json:"risks,omitempty"`
	Rollback     string      `json:"rollback,omitempty"`
	CreatedAt    string      `json:"created_at"`
	PrevHash     string      `json:"prev_hash"`
	Hash         string      `json:"hash,omitempty"`
}

type Operation struct {
	Op    string `json:"op"`
	ID    string `json:"id,omitempty"`
	From  string `json:"from,omitempty"`
	To    string `json:"to,omitempty"`
	RunID string `json:"run_id,omitempty"`
}

func (c Changeset) Validate() error {
	if !ValidChangesetID(c.ChangesetID) {
		return fmt.Errorf("invalid changeset_id %q", c.ChangesetID)
	}
	if c.Project == "" {
		return errors.New("changeset project is required")
	}
	if strings.TrimSpace(c.Intent) == "" {
		return errors.New("changeset intent is required")
	}
	if len(c.Operations) == 0 {
		return errors.New("changeset has no operations")
	}
	for i, op := range c.Operations {
		switch op.Op {
		case "task.update":
			if op.ID == "" {
				return fmt.Errorf("operation %d: task.update requires id", i)
			}
			if !ValidTaskStatus(op.From) || !ValidTaskStatus(op.To) {
				return fmt.Errorf("operation %d: invalid task status transition %q -> %q", i, op.From, op.To)
			}
		case "run.append":
			if op.RunID == "" {
				return fmt.Errorf("operation %d: run.append requires run_id", i)
			}
		default:
			return fmt.Errorf("operation %d: unsupported op %q", i, op.Op)
		}
	}
	return nil
}

// Receipt is written after a changeset is applied.
type Receipt struct {
	ChangesetID   string `json:"changeset_id"`
	Project       string `json:"project"`
	ChangesetHash string `json:"changeset_hash"`
	AppliedAt     string `json:"applied_at"`
	GitCommit     string `json:"git_commit"`
	PrevCommit    string `json:"prev_commit"`
	Revision      string `json:"revision"`
	Status        string `json:"status"`
}

func (r Receipt) Validate() error {
	if r.ChangesetID == "" || r.ChangesetHash == "" || r.Revision == "" {
		return errors.New("receipt is missing required fields")
	}
	if !ValidHash(r.ChangesetHash) || !ValidHash(r.Revision) {
		return errors.New("receipt contains an invalid hash")
	}
	if r.Status != "applied" {
		return fmt.Errorf("receipt has unexpected status %q", r.Status)
	}
	return nil
}

// Gate is one line in progress/gates.jsonl.
type Gate struct {
	GateID         string `json:"gate_id"`
	Project        string `json:"project"`
	ChangesetID    string `json:"changeset_id"`
	ChangesetHash  string `json:"changeset_hash"`
	ApprovedBy     string `json:"approved_by"`
	ApprovedAt     string `json:"approved_at"`
	Gate           string `json:"gate,omitempty"`
	Note           string `json:"note,omitempty"`
	ApprovalMethod string `json:"approval_method,omitempty"`
}

func (g Gate) Validate() error {
	if g.ChangesetID == "" || g.ChangesetHash == "" || g.ApprovedBy == "" {
		return errors.New("gate is missing required fields")
	}
	if !ValidHash(g.ChangesetHash) {
		return errors.New("gate contains an invalid changeset hash")
	}
	switch g.ApprovalMethod {
	case "", "tty", "token", "legacy":
	default:
		return fmt.Errorf("gate has unsupported approval_method %q", g.ApprovalMethod)
	}
	return nil
}

// RunSpec is progress/runs/<run_id>/run_spec.json.
type RunSpec struct {
	RunID      string            `json:"run_id"`
	Project    string            `json:"project"`
	TaskID     string            `json:"task_id"`
	Method     string            `json:"method"`
	Split      string            `json:"split"`
	Seed       int               `json:"seed"`
	Host       string            `json:"host,omitempty"`
	Workdir    string            `json:"workdir,omitempty"`
	RunRoot    string            `json:"run_root,omitempty"`
	Python     string            `json:"python,omitempty"`
	Command    string            `json:"command"`
	Env        map[string]string `json:"env,omitempty"`
	Inputs     []InputRef        `json:"inputs,omitempty"`
	Outputs    []string          `json:"outputs,omitempty"`
	Code       CodeInfo          `json:"code"`
	ConfigHash string            `json:"config_hash"`
	ConfigPath string            `json:"config_path,omitempty"`
	Data       *DataRef          `json:"data,omitempty"`
	CreatedAt  string            `json:"created_at"`
	Revision   string            `json:"revision"`
}

type InputRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256,omitempty"`
}

type DataRef struct {
	Path   string `json:"path,omitempty"`
	URI    string `json:"uri,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
}

type CodeInfo struct {
	Repo      string `json:"repo,omitempty"`
	Commit    string `json:"commit,omitempty"`
	PatchHash string `json:"patch_hash,omitempty"`
	Dirty     bool   `json:"dirty"`
}

func (r RunSpec) Validate() error {
	if !ValidRunID(r.RunID) {
		return fmt.Errorf("invalid run_id %q", r.RunID)
	}
	if r.Project == "" || r.TaskID == "" {
		return errors.New("run spec is missing project or task_id")
	}
	if r.Command == "" {
		return errors.New("run spec command is required")
	}
	return nil
}

// Artifact is a remote object referenced by URI + SHA-256.
type Artifact struct {
	Name   string `json:"name"`
	URI    string `json:"uri"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size,omitempty"`
}

// RemoteResult is the JSON returned by a runner subagent.
type RemoteResult struct {
	RunID           string            `json:"run_id"`
	Status          string            `json:"status"`
	ExitCode        int               `json:"exit_code"`
	StartedAt       string            `json:"started_at,omitempty"`
	FinishedAt      string            `json:"finished_at,omitempty"`
	Metrics         map[string]any    `json:"metrics,omitempty"`
	Artifacts       []Artifact        `json:"artifacts,omitempty"`
	Manifest        map[string]any    `json:"manifest,omitempty"`
	LogsTail        string            `json:"logs_tail,omitempty"`
	Error           string            `json:"error,omitempty"`
	Environment     map[string]string `json:"environment,omitempty"`
	EnvironmentHash string            `json:"environment_hash,omitempty"`
	Code            CodeInfo          `json:"code,omitempty"`
	ConfigHash      string            `json:"config_hash,omitempty"`
	Data            *DataRef          `json:"data,omitempty"`
}

func (r *RemoteResult) Normalize() {
	if r.Status == "" {
		if r.ExitCode == 0 {
			r.Status = RunSucceeded
		} else {
			r.Status = RunFailed
		}
	}
}

func (r RemoteResult) Validate() error {
	if r.Status != RunSucceeded && r.Status != RunFailed && r.Status != RunCancelled {
		return fmt.Errorf("invalid remote result status %q", r.Status)
	}
	for i, a := range r.Artifacts {
		if a.Name == "" {
			return fmt.Errorf("artifact %d: name is required", i)
		}
		if a.URI == "" && a.SHA256 == "" {
			return fmt.Errorf("artifact %s: uri or sha256 is required", a.Name)
		}
	}
	if r.EnvironmentHash != "" && !ValidHash(r.EnvironmentHash) {
		return fmt.Errorf("invalid environment_hash %q", r.EnvironmentHash)
	}
	return nil
}

// RunResult is the normalized progress/runs/<run_id>/result.json.
type RunResult struct {
	RunID           string            `json:"run_id"`
	Project         string            `json:"project"`
	TaskID          string            `json:"task_id"`
	Status          string            `json:"status"`
	ExitCode        int               `json:"exit_code"`
	StartedAt       string            `json:"started_at,omitempty"`
	FinishedAt      string            `json:"finished_at,omitempty"`
	RecordedAt      string            `json:"recorded_at"`
	Metrics         map[string]any    `json:"metrics"`
	Artifacts       []Artifact        `json:"artifacts"`
	LogsTail        string            `json:"logs_tail,omitempty"`
	Error           string            `json:"error,omitempty"`
	Environment     map[string]string `json:"environment,omitempty"`
	EnvironmentHash string            `json:"environment_hash,omitempty"`
	Code            CodeInfo          `json:"code"`
	ConfigHash      string            `json:"config_hash,omitempty"`
	Data            *DataRef          `json:"data,omitempty"`
	Manifest        *Manifest         `json:"manifest,omitempty"`
}

// Manifest is a local integrity manifest for a run directory.
type Manifest struct {
	RunID     string         `json:"run_id"`
	Project   string         `json:"project"`
	CreatedAt string         `json:"created_at"`
	Files     []ManifestFile `json:"files"`
	Artifacts []Artifact     `json:"artifacts,omitempty"`
}

type ManifestFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
