package core

import (
	"os"
	"path/filepath"
	"time"

	"github.com/buerka/lemma/internal/schema"
	"gopkg.in/yaml.v3"
)

// Ledger is an opened progress/ directory.
type Ledger struct {
	Root    string
	Dir     string
	Project schema.Project

	now func() time.Time
}

const (
	projectFile   = "project.yaml"
	tasksFile     = "tasks.yaml"
	eventsFile    = "events.jsonl"
	gatesFile     = "gates.jsonl"
	stateFile     = "STATE.md"
	reviewFile    = "REVIEW.md"
	decisionsFile = "DECISIONS.md"
	gitignoreFile = ".gitignore"
)

// Open loads a ledger from <root>/<ledger>. It validates the project schema
// version and tasks.yaml.
func Open(root, ledger string) (*Ledger, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return nil, Wrap(CodeGeneral, "resolve project root", err)
	}
	absRoot = filepath.Clean(absRoot)
	dir := ledger
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(absRoot, dir)
	}
	dir = filepath.Clean(dir)

	raw, err := os.ReadFile(filepath.Join(dir, projectFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Errorf(CodeNotFound, "no ledger at %s (run lem init first)", dir)
		}
		return nil, Wrap(CodeGeneral, "read project.yaml", err)
	}
	var p schema.Project
	if err := yaml.Unmarshal(raw, &p); err != nil {
		return nil, Wrap(CodeGeneral, "parse project.yaml", err)
	}
	if p.SchemaVersion != schema.SchemaVersion {
		return nil, Errorf(CodeStale, "schema version %d is not supported (expected %d); run lem migrate", p.SchemaVersion, schema.SchemaVersion)
	}
	if err := p.Validate(); err != nil {
		return nil, Wrap(CodeGeneral, "invalid project.yaml", err)
	}
	l := &Ledger{Root: absRoot, Dir: dir, Project: p}
	if _, err := l.LoadTasks(); err != nil {
		return nil, err
	}
	return l, nil
}

type InitOptions struct {
	Root      string
	Ledger    string
	Project   schema.Project
	Now       time.Time
	LockWait  time.Duration
	NoGitInit bool
}

// InitLedger creates progress/ as an independent git repository and writes the
// genesis event and initial state.
func InitLedger(opts InitOptions) (*Ledger, schema.Event, error) {
	if opts.Project.SchemaVersion == 0 {
		opts.Project.SchemaVersion = schema.SchemaVersion
	}
	if err := opts.Project.Validate(); err != nil {
		return nil, schema.Event{}, Wrap(CodeGeneral, "invalid project configuration", err)
	}
	if opts.Ledger == "" {
		opts.Ledger = "progress"
	}
	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, schema.Event{}, Wrap(CodeGeneral, "resolve project root", err)
	}
	root = filepath.Clean(root)
	dir := opts.Ledger
	if !filepath.IsAbs(dir) {
		dir = filepath.Join(root, dir)
	}
	dir = filepath.Clean(dir)

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, schema.Event{}, Wrap(CodeGeneral, "create ledger directory", err)
	}
	lock, err := AcquireLock(dir, opts.LockWait)
	if err != nil {
		return nil, schema.Event{}, err
	}
	defer func() { _ = lock.Release() }()

	if _, err := os.Stat(filepath.Join(dir, projectFile)); err == nil {
		return nil, schema.Event{}, Errorf(CodeConflict, "ledger already initialized at %s", dir)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, schema.Event{}, Wrap(CodeGeneral, "read ledger directory", err)
	}
	for _, e := range entries {
		if e.Name() == ".lock" || e.Name() == ".git" {
			continue
		}
		return nil, schema.Event{}, Errorf(CodeConflict, "refusing to initialize non-empty directory %s (found %s)", dir, e.Name())
	}

	for _, sub := range []string{"changesets", "receipts", "runs"} {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, schema.Event{}, Wrap(CodeGeneral, "create ledger subdirectory", err)
		}
	}

	projectYAML, err := marshalYAML(opts.Project)
	if err != nil {
		return nil, schema.Event{}, err
	}
	tasksYAML, err := marshalYAML(schema.TasksFile{Tasks: []schema.Task{}})
	if err != nil {
		return nil, schema.Event{}, err
	}
	files := map[string]struct {
		data []byte
		perm os.FileMode
	}{
		projectFile:   {projectYAML, 0o644},
		tasksFile:     {tasksYAML, 0o644},
		eventsFile:    {[]byte{}, 0o644},
		gatesFile:     {[]byte{}, 0o644},
		decisionsFile: {[]byte("# Decisions\n\nRecord durable decisions here. Changes that alter task status should go through plan / approve / apply.\n"), 0o644},
		gitignoreFile: {[]byte(".lock\n*.tmp\n*.tmp-*\n"), 0o644},
	}
	for name, f := range files {
		if err := AtomicWrite(filepath.Join(dir, name), f.data, f.perm); err != nil {
			return nil, schema.Event{}, Wrap(CodeGeneral, "write "+name, err)
		}
	}

	l := &Ledger{Root: root, Dir: dir, Project: opts.Project}
	if !opts.NoGitInit {
		if err := (Git{Dir: dir}).Init(); err != nil {
			return nil, schema.Event{}, Wrap(CodeGeneral, "initialize progress git repository", err)
		}
	}

	if !opts.Now.IsZero() {
		l.now = func() time.Time { return opts.Now }
	}
	ev, err := l.AppendEvent("project.init", "", "", "", map[string]any{
		"schema_version": schema.SchemaVersion,
		"host":           opts.Project.Remote.Host,
		"workdir":        opts.Project.Remote.Workdir,
	})
	if err != nil {
		return nil, schema.Event{}, err
	}
	if _, err := l.WriteState(true); err != nil {
		return nil, schema.Event{}, err
	}
	if !opts.NoGitInit {
		if _, _, err := (Git{Dir: dir}).Commit("init: initialize " + opts.Project.Project + " ledger"); err != nil {
			return nil, schema.Event{}, Wrap(CodeGeneral, "commit initial ledger", err)
		}
	}
	return l, ev, nil
}

// now is injectable in tests. nil means time.Now.
var _ = time.Now

func (l *Ledger) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

func (l *Ledger) NowUTC() string {
	return l.clock().Format(time.RFC3339)
}
