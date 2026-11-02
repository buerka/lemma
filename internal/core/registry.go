package core

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/buerka/lemma/internal/schema"
	"gopkg.in/yaml.v3"
)

// Registry is ~/.config/lem/projects.yaml (or $LEM_CONFIG_DIR/projects.yaml).
type Registry struct {
	Projects map[string]RegistryProject `yaml:"projects"`
	Default  string                     `yaml:"default,omitempty"`
}

type RegistryProject struct {
	Root   string `yaml:"root" json:"root"`
	Ledger string `yaml:"ledger,omitempty" json:"ledger,omitempty"`
}

type Resolved struct {
	ID       string `json:"id"`
	Root     string `json:"root"`
	Ledger   string `json:"ledger"`
	Dir      string `json:"dir"`
	Source   string `json:"source"`
	Registry string `json:"registry,omitempty"`
}

func ConfigDir() string {
	if v := os.Getenv("LEM_CONFIG_DIR"); v != "" {
		return v
	}
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "lem")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".lem"
	}
	return filepath.Join(home, ".config", "lem")
}

func RegistryPath() string { return filepath.Join(ConfigDir(), "projects.yaml") }

func LoadRegistry() (*Registry, error) {
	r := &Registry{Projects: map[string]RegistryProject{}}
	raw, err := os.ReadFile(RegistryPath())
	if err != nil {
		if os.IsNotExist(err) {
			return r, nil
		}
		return nil, Wrap(CodeGeneral, "read project registry", err)
	}
	if err := yaml.Unmarshal(raw, r); err != nil {
		return nil, Wrap(CodeGeneral, "parse project registry", err)
	}
	if r.Projects == nil {
		r.Projects = map[string]RegistryProject{}
	}
	return r, nil
}

func saveRegistry(r *Registry) error {
	b, err := marshalYAML(r)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ConfigDir(), 0o755); err != nil {
		return Wrap(CodeGeneral, "create config directory", err)
	}
	return AtomicWrite(RegistryPath(), b, 0o644)
}

func AddProject(id, root, ledger string, setDefault, force bool, lockTimeout time.Duration) (*Registry, error) {
	id = strings.TrimSpace(id)
	if id == "" || strings.ContainsAny(id, "/\\ ") {
		return nil, Errorf(CodeUsage, "invalid project id %q", id)
	}
	if ledger == "" {
		ledger = "progress"
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, Wrap(CodeUsage, "resolve project root", err)
	}
	abs = filepath.Clean(abs)
	ledgerDir := ledger
	if !filepath.IsAbs(ledgerDir) {
		ledgerDir = filepath.Join(abs, ledger)
	}
	p, err := readProjectFile(ledgerDir)
	if err != nil {
		return nil, err
	}

	lock, err := AcquireLock(ConfigDir(), lockTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()

	r, err := LoadRegistry()
	if err != nil {
		return nil, err
	}
	if existing, ok := r.Projects[id]; ok {
		existingAbs, _ := filepath.Abs(existing.Root)
		if filepath.Clean(existingAbs) != abs && !force {
			return nil, Errorf(CodeConflict, "project %q already points to %s (use --force to replace)", id, existing.Root)
		}
	}
	r.Projects[id] = RegistryProject{Root: abs, Ledger: ledger}
	if setDefault || r.Default == "" {
		r.Default = id
	}
	if err := saveRegistry(r); err != nil {
		return nil, err
	}
	_ = p
	return r, nil
}

func RemoveProject(id string, lockTimeout time.Duration) (*Registry, error) {
	lock, err := AcquireLock(ConfigDir(), lockTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()

	r, err := LoadRegistry()
	if err != nil {
		return nil, err
	}
	if _, ok := r.Projects[id]; !ok {
		return nil, Errorf(CodeNotFound, "project %q is not registered", id)
	}
	delete(r.Projects, id)
	if r.Default == id {
		r.Default = ""
		keys := make([]string, 0, len(r.Projects))
		for k := range r.Projects {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			r.Default = keys[0]
		}
	}
	if err := saveRegistry(r); err != nil {
		return nil, err
	}
	return r, nil
}

func readProjectFile(ledgerDir string) (*schema.Project, error) {
	raw, err := os.ReadFile(filepath.Join(ledgerDir, projectFile))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, Errorf(CodeNotFound, "no progress/project.yaml under %s; run lem init first", filepath.Dir(ledgerDir))
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
	return &p, nil
}

// ResolveProject resolves a project reference in the documented order handled
// by the caller: explicit flag/env, upward discovery, registry default.
func ResolveProject(ref, cwd string) (*Resolved, error) {
	if ref != "" {
		return resolveRef(ref, cwd)
	}
	if r, err := discoverUpward(cwd); err == nil {
		return r, nil
	}
	r, err := LoadRegistry()
	if err != nil {
		return nil, err
	}
	if r.Default != "" {
		if p, ok := r.Projects[r.Default]; ok {
			return resolvedFromRegistry(r.Default, p)
		}
	}
	return nil, Errorf(CodeNotFound, "no project found; run lem init, pass --project, or set LEM_PROJECT")
}

func resolveRef(ref, cwd string) (*Resolved, error) {
	r, err := LoadRegistry()
	if err == nil {
		if p, ok := r.Projects[ref]; ok {
			return resolvedFromRegistry(ref, p)
		}
	}
	candidate := ref
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(cwd, candidate)
	}
	if st, err := os.Stat(candidate); err == nil && st.IsDir() {
		return resolvedFromDir(candidate, "path")
	}
	return nil, Errorf(CodeNotFound, "unknown project %q (not in registry and not a directory)", ref)
}

func resolvedFromRegistry(id string, p RegistryProject) (*Resolved, error) {
	root, err := filepath.Abs(p.Root)
	if err != nil {
		return nil, Wrap(CodeGeneral, "resolve registered project root", err)
	}
	ledger := p.Ledger
	if ledger == "" {
		ledger = "progress"
	}
	ledgerDir := ledger
	if !filepath.IsAbs(ledgerDir) {
		ledgerDir = filepath.Join(root, ledger)
	}
	if _, err := os.Stat(filepath.Join(ledgerDir, projectFile)); err != nil {
		return nil, Errorf(CodeNotFound, "registered project %q has no ledger at %s", id, ledgerDir)
	}
	return &Resolved{ID: id, Root: root, Ledger: ledger, Dir: ledgerDir, Source: "registry", Registry: RegistryPath()}, nil
}

func resolvedFromDir(dir, source string) (*Resolved, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, Wrap(CodeGeneral, "resolve project directory", err)
	}
	abs = filepath.Clean(abs)

	// The path itself is a ledger directory.
	if _, err := os.Stat(filepath.Join(abs, projectFile)); err == nil {
		p, err := readProjectFile(abs)
		if err != nil {
			return nil, err
		}
		return &Resolved{ID: p.Project, Root: filepath.Dir(abs), Ledger: filepath.Base(abs), Dir: abs, Source: source}, nil
	}
	// The path is a project root containing progress/.
	ledgerDir := filepath.Join(abs, "progress")
	p, err := readProjectFile(ledgerDir)
	if err != nil {
		return nil, err
	}
	return &Resolved{ID: p.Project, Root: abs, Ledger: "progress", Dir: ledgerDir, Source: source}, nil
}

func discoverUpward(cwd string) (*Resolved, error) {
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return nil, Wrap(CodeGeneral, "resolve working directory", err)
	}
	for dir := filepath.Clean(abs); ; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "progress", projectFile)); err == nil {
			return resolvedFromDir(dir, "cwd")
		}
		if filepath.Base(dir) == "progress" {
			if _, err := os.Stat(filepath.Join(dir, projectFile)); err == nil {
				return resolvedFromDir(dir, "cwd")
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
	}
	return nil, Errorf(CodeNotFound, "no progress/project.yaml found")
}

func (r *Registry) SortedIDs() []string {
	ids := make([]string, 0, len(r.Projects))
	for id := range r.Projects {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}
