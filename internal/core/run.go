package core

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/buerka/lemma/internal/schema"
	tmpl "github.com/buerka/lemma/internal/templates"
)

type StartRunOptions struct {
	TaskID           string
	Method           string
	Split            string
	Seed             int
	Command          string
	RunID            string
	Config           string
	Data             string
	DataSHA256       string
	Env              map[string]string
	Outputs          []string
	Force            bool
	ExpectedRevision string
	LockTimeout      time.Duration
}

type StartRunResult struct {
	Spec           schema.RunSpec `json:"run_spec"`
	Dir            string         `json:"dir"`
	SpecPath       string         `json:"spec_path"`
	RemoteTaskPath string         `json:"remote_task"`
	AlreadyExists  bool           `json:"already_exists"`
}

func StartRun(l *Ledger, opts StartRunOptions) (*StartRunResult, error) {
	lock, err := AcquireLock(l.Dir, opts.LockTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()
	if err := EnsureClean(l); err != nil {
		return nil, err
	}

	tasks, err := l.LoadTasks()
	if err != nil {
		return nil, err
	}
	if opts.TaskID == "" {
		return nil, Errorf(CodeUsage, "run start requires --task")
	}
	task := tasks.Find(opts.TaskID)
	if task == nil {
		return nil, Errorf(CodeNotFound, "task %s not found", opts.TaskID)
	}
	if opts.Method == "" {
		opts.Method = "default"
	}
	if opts.Split == "" {
		opts.Split = "default"
	}
	runID := opts.RunID
	if runID == "" {
		runID = sanitizeRunID(fmt.Sprintf("%s.%s.%s.seed%d", opts.TaskID, opts.Method, opts.Split, opts.Seed))
	}
	if !schema.ValidRunID(runID) {
		return nil, Errorf(CodeUsage, "invalid run id %q", runID)
	}
	runDir := l.RunDir(runID)
	specPath := filepath.Join(runDir, "run_spec.json")
	if _, err := os.Stat(specPath); err == nil {
		if !opts.Force {
			spec, err := l.LoadRunSpec(runID)
			if err != nil {
				return nil, err
			}
			return &StartRunResult{Spec: *spec, Dir: runDir, SpecPath: specPath, RemoteTaskPath: filepath.Join(runDir, "REMOTE_TASK.md"), AlreadyExists: true}, nil
		}
	}
	if err := CheckExpectedRevision(l, opts.ExpectedRevision); err != nil {
		return nil, err
	}

	rev, err := l.Revision()
	if err != nil {
		return nil, err
	}
	python := l.Project.Remote.Python
	if python == "" {
		python = "python3"
	}
	if opts.Command == "" {
		opts.Command = fmt.Sprintf("%s train.py --config configs/%s.yaml --seed %d", python, opts.Method, opts.Seed)
	}
	command := opts.Command
	code := l.codeInfo()
	configHash, inputs, configPath, err := l.configInfo(opts)
	if err != nil {
		return nil, err
	}
	data, dataInput, err := l.dataInfo(opts)
	if err != nil {
		return nil, err
	}
	if dataInput != nil {
		inputs = append(inputs, *dataInput)
	}
	env := map[string]string{"PYTHONHASHSEED": strconv.Itoa(opts.Seed)}
	for k, v := range opts.Env {
		env[k] = v
	}
	outputs := opts.Outputs
	if len(outputs) == 0 {
		outputs = []string{"metrics.json", "manifest.json", "result.json", "checkpoints/"}
	}
	spec := schema.RunSpec{
		RunID:      runID,
		Project:    l.Project.Project,
		TaskID:     opts.TaskID,
		Method:     opts.Method,
		Split:      opts.Split,
		Seed:       opts.Seed,
		Host:       l.Project.Remote.Host,
		Workdir:    l.Project.Remote.Workdir,
		RunRoot:    l.Project.Remote.RunRoot,
		Python:     python,
		Command:    command,
		Env:        env,
		Inputs:     inputs,
		Outputs:    outputs,
		Code:       code,
		ConfigHash: configHash,
		ConfigPath: configPath,
		Data:       data,
		CreatedAt:  l.NowUTC(),
		Revision:   rev,
	}
	if err := spec.Validate(); err != nil {
		return nil, Wrap(CodeGeneral, "invalid run spec", err)
	}

	createdDir := false
	if _, err := os.Stat(runDir); os.IsNotExist(err) {
		createdDir = true
	}
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return nil, Wrap(CodeGeneral, "create run directory", err)
	}
	if opts.Force {
		// A forced restart must not leave the previous run's result behind.
		// Do this only after all inputs have been validated.
		for _, name := range []string{"result.json", "metrics.json", "manifest.json", "remote_result.json", "exit_code", "stdout.log"} {
			_ = os.Remove(filepath.Join(runDir, name))
		}
	}
	ok := false
	defer func() {
		if !ok && createdDir {
			_ = os.RemoveAll(runDir)
		}
	}()

	specBytes, err := MarshalIndentNoEscape(spec)
	if err != nil {
		return nil, Wrap(CodeGeneral, "encode run_spec.json", err)
	}
	if err := AtomicWrite(specPath, specBytes, 0o644); err != nil {
		return nil, Wrap(CodeGeneral, "write run_spec.json", err)
	}
	remotePath := l.Project.Remote.RunRoot
	if remotePath == "" {
		remotePath = "~/.lem-runs"
	}
	dataRemote := tmpl.RemoteTaskData{
		RunID:       runID,
		Project:     l.Project.Project,
		TaskID:      opts.TaskID,
		Host:        l.Project.Remote.Host,
		RemoteDir:   strings.TrimRight(remotePath, "/") + "/" + runID,
		LocalRunDir: l.Rel(runDir),
		LocalSpec:   l.Rel(specPath),
		Python:      python,
		Command:     command,
		CreatedAt:   spec.CreatedAt,
		Outputs:     outputs,
	}
	for _, in := range inputs {
		dataRemote.Inputs = append(dataRemote.Inputs, tmpl.RemoteInput{Path: in.Path, SHA256: in.SHA256})
	}
	remoteTask, err := tmpl.RenderRemoteTask(dataRemote)
	if err != nil {
		return nil, Wrap(CodeGeneral, "render REMOTE_TASK.md", err)
	}
	if err := AtomicWrite(filepath.Join(runDir, "REMOTE_TASK.md"), remoteTask, 0o644); err != nil {
		return nil, Wrap(CodeGeneral, "write REMOTE_TASK.md", err)
	}
	runnerSh, err := tmpl.RunnerShell()
	if err != nil {
		return nil, Wrap(CodeGeneral, "load runner.sh", err)
	}
	if err := AtomicWrite(filepath.Join(runDir, "runner.sh"), runnerSh, 0o755); err != nil {
		return nil, Wrap(CodeGeneral, "write runner.sh", err)
	}
	runnerPy, err := tmpl.RunnerPython()
	if err != nil {
		return nil, Wrap(CodeGeneral, "load runner.py", err)
	}
	if err := AtomicWrite(filepath.Join(runDir, "runner.py"), runnerPy, 0o755); err != nil {
		return nil, Wrap(CodeGeneral, "write runner.py", err)
	}

	if task.Status == schema.StatusTodo {
		task.Status = schema.StatusDoing
		task.UpdatedAt = l.NowUTC()
		if err := l.SaveTasks(tasks); err != nil {
			return nil, err
		}
	}
	_, err = l.AppendEvent("run.start", opts.TaskID, "", runID, map[string]any{
		"run_id":          runID,
		"method":          opts.Method,
		"split":           opts.Split,
		"seed":            opts.Seed,
		"config_hash":     configHash,
		"code_commit":     code.Commit,
		"code_patch_hash": code.PatchHash,
		"dirty":           code.Dirty,
		"data_sha256":     dataSHA(data),
		"host":            l.Project.Remote.Host,
		"local_workdir":   l.LocalWorkdir(),
		"workdir":         l.Project.Remote.Workdir,
	})
	if err != nil {
		return nil, err
	}
	if _, err := l.WriteState(true); err != nil {
		return nil, err
	}
	if _, _, err := l.Commit("run: start " + runID); err != nil {
		return nil, Wrap(CodeGeneral, "commit run start", err)
	}
	ok = true
	return &StartRunResult{Spec: spec, Dir: runDir, SpecPath: specPath, RemoteTaskPath: filepath.Join(runDir, "REMOTE_TASK.md")}, nil
}

type FinishRunOptions struct {
	ResultPath       string
	Force            bool
	ExpectedRevision string
	LockTimeout      time.Duration
}

type FinishRunResult struct {
	Result   schema.RunResult `json:"result"`
	Manifest schema.Manifest  `json:"manifest"`
	Dir      string           `json:"dir"`
	Already  bool             `json:"already_finished"`
}

func FinishRun(l *Ledger, runID string, opts FinishRunOptions) (*FinishRunResult, error) {
	lock, err := AcquireLock(l.Dir, opts.LockTimeout)
	if err != nil {
		return nil, err
	}
	defer func() { _ = lock.Release() }()
	if err := EnsureCleanExcept(l, filepath.Join("runs", runID)); err != nil {
		return nil, err
	}

	spec, err := l.LoadRunSpec(runID)
	if err != nil {
		return nil, err
	}
	runDir := l.RunDir(runID)
	resultPath := filepath.Join(runDir, "result.json")
	if existing, err := LoadRunResult(l, runID); err == nil && existing != nil && existing.RecordedAt != "" {
		if !opts.Force {
			manifest, err := LoadManifest(l, runID)
			if err != nil {
				return nil, err
			}
			return &FinishRunResult{Result: *existing, Manifest: *manifest, Dir: runDir, Already: true}, nil
		}
	}
	if err := CheckExpectedRevision(l, opts.ExpectedRevision); err != nil {
		return nil, err
	}

	remotePath := resolveExistingPath(l.Root, opts.ResultPath)
	if remotePath == "" {
		return nil, Errorf(CodeNotFound, "remote result not found: %s", opts.ResultPath)
	}
	raw, err := os.ReadFile(remotePath)
	if err != nil {
		return nil, Wrap(CodeGeneral, "read remote result", err)
	}
	if !WithinDir(runDir, remotePath) {
		// Keep the audit trail self-contained: a result passed from outside the
		// run directory is copied into it before it is committed.
		local := filepath.Join(runDir, "remote_result.json")
		if err := AtomicWrite(local, raw, 0o644); err != nil {
			return nil, Wrap(CodeGeneral, "copy remote result into run directory", err)
		}
		remotePath = local
	}
	var rr schema.RemoteResult
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, Wrap(CodeGeneral, "parse remote_result.json", err)
	}
	if rr.RunID != "" && rr.RunID != runID {
		return nil, Errorf(CodeConflict, "remote result run_id %q does not match %q", rr.RunID, runID)
	}
	rr.RunID = runID
	rr.Normalize()
	if err := rr.Validate(); err != nil {
		return nil, Wrap(CodeGeneral, "invalid remote result", err)
	}
	if rr.Code == (schema.CodeInfo{}) {
		rr.Code = spec.Code
	}
	if rr.ConfigHash == "" {
		rr.ConfigHash = spec.ConfigHash
	}
	if rr.Data == nil {
		rr.Data = spec.Data
	}
	metrics := rr.Metrics
	if metrics == nil {
		metrics = map[string]any{}
	}
	artifacts := rr.Artifacts
	if artifacts == nil {
		artifacts = []schema.Artifact{}
	}

	metricsBytes, err := MarshalIndentNoEscape(metrics)
	if err != nil {
		return nil, Wrap(CodeGeneral, "encode metrics.json", err)
	}
	if err := AtomicWrite(filepath.Join(runDir, "metrics.json"), metricsBytes, 0o644); err != nil {
		return nil, Wrap(CodeGeneral, "write metrics.json", err)
	}

	manifest, err := buildManifest(l, runID, artifacts)
	if err != nil {
		return nil, err
	}
	manifest.EnvironmentError = rr.EnvironmentError
	manifest.EnvironmentNotes = rr.EnvironmentNotes
	manifestBytes, err := MarshalIndentNoEscape(manifest)
	if err != nil {
		return nil, Wrap(CodeGeneral, "encode manifest.json", err)
	}
	if err := AtomicWrite(filepath.Join(runDir, "manifest.json"), manifestBytes, 0o644); err != nil {
		return nil, Wrap(CodeGeneral, "write manifest.json", err)
	}

	environmentHash := rr.EnvironmentHash
	if environmentHash == "" && len(rr.Environment) > 0 {
		h, err := HashValue(rr.Environment)
		if err != nil {
			return nil, Wrap(CodeGeneral, "hash remote environment", err)
		}
		environmentHash = h
	}
	result := schema.RunResult{
		RunID:            runID,
		Project:          l.Project.Project,
		TaskID:           spec.TaskID,
		Status:           rr.Status,
		ExitCode:         rr.ExitCode,
		StartedAt:        rr.StartedAt,
		FinishedAt:       rr.FinishedAt,
		RecordedAt:       l.NowUTC(),
		Metrics:          metrics,
		Artifacts:        artifacts,
		LogsTail:         rr.LogsTail,
		Error:            rr.Error,
		Environment:      rr.Environment,
		EnvironmentHash:  environmentHash,
		EnvironmentError: rr.EnvironmentError,
		EnvironmentNotes: rr.EnvironmentNotes,
		Code:             rr.Code,
		ConfigHash:       rr.ConfigHash,
		Data:             rr.Data,
		Manifest:         manifest,
	}
	resultBytes, err := MarshalIndentNoEscape(result)
	if err != nil {
		return nil, Wrap(CodeGeneral, "encode result.json", err)
	}
	if err := AtomicWrite(resultPath, resultBytes, 0o644); err != nil {
		return nil, Wrap(CodeGeneral, "write result.json", err)
	}

	ev, err := l.AppendEvent("run.finish", spec.TaskID, "", runID, map[string]any{
		"status":            rr.Status,
		"exit_code":         rr.ExitCode,
		"metrics":           metrics,
		"artifact_count":    len(artifacts),
		"manifest_files":    len(manifest.Files),
		"config_hash":       rr.ConfigHash,
		"data_sha256":       dataSHA(rr.Data),
		"code_commit":       rr.Code.Commit,
		"code_patch_hash":   rr.Code.PatchHash,
		"code_dirty":        rr.Code.Dirty,
		"environment_hash":  environmentHash,
		"environment_error": rr.EnvironmentError,
		"remote_result":     l.Rel(remotePath),
	})
	if err != nil {
		return nil, err
	}
	_ = ev
	if _, err := l.WriteState(true); err != nil {
		return nil, err
	}
	if _, _, err := l.Commit("run: finish " + runID + " " + rr.Status); err != nil {
		return nil, Wrap(CodeGeneral, "commit run finish", err)
	}
	return &FinishRunResult{Result: result, Manifest: *manifest, Dir: runDir}, nil
}

func (l *Ledger) codeInfo() schema.CodeInfo {
	repo := l.codeRoot()
	if repo == "" {
		return schema.CodeInfo{}
	}
	g := Git{Dir: repo}
	commit, _ := g.Head()
	status := gitStatusExcluding(g, l.Dir)
	if strings.TrimSpace(status) == "" {
		return schema.CodeInfo{Repo: repo, Commit: commit}
	}
	diff, _ := g.run("diff", "HEAD")
	patch := diff + "\n--status--\n" + status
	return schema.CodeInfo{Repo: repo, Commit: commit, PatchHash: HashFileBytes([]byte(patch)), Dirty: true}
}

// codeRoot returns the local git repository used to record run reproducibility.
// The order is local.workdir -> remote.workdir (only if it exists locally) ->
// project root. remote.workdir is never assumed to be a local path.
func (l *Ledger) codeRoot() string {
	for _, candidate := range l.localCodeCandidates() {
		if repo := GitTopLevel(candidate); repo != "" {
			return repo
		}
	}
	return ""
}

// LocalWorkdir returns the configured local clone path, expanded and resolved
// relative to the project root when necessary. It is empty when unset.
func (l *Ledger) LocalWorkdir() string {
	return resolveConfiguredPath(l.Root, l.Project.Local.Workdir)
}

// RemoteWorkdirPath returns the configured remote path as a local candidate.
// Callers must check os.Stat before treating it as locally accessible.
func (l *Ledger) RemoteWorkdirPath() string {
	return resolveConfiguredPath(l.Root, l.Project.Remote.Workdir)
}

func resolveConfiguredPath(root, p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	p = expandHome(p)
	if !filepath.IsAbs(p) {
		p = filepath.Join(root, p)
	}
	return filepath.Clean(p)
}

func (l *Ledger) localCodeCandidates() []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		st, err := os.Stat(p)
		if err != nil || !st.IsDir() {
			return
		}
		for _, existing := range out {
			if existing == p {
				return
			}
		}
		out = append(out, p)
	}
	add(l.LocalWorkdir())
	add(l.RemoteWorkdirPath())
	add(l.Root)
	return out
}

func (l *Ledger) inputCandidates() []string {
	var out []string
	add := func(p string) {
		if p == "" {
			return
		}
		for _, existing := range out {
			if existing == p {
				return
			}
		}
		out = append(out, p)
	}
	add(l.LocalWorkdir())
	if remote := l.RemoteWorkdirPath(); remote != "" {
		if st, err := os.Stat(remote); err == nil && st.IsDir() {
			add(remote)
		}
	}
	add(l.Root)
	return out
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			if p == "~" {
				return home
			}
			return filepath.Join(home, strings.TrimPrefix(p, "~/"))
		}
	}
	return p
}

func gitStatusExcluding(g Git, ledgerDir string) string {
	out, err := g.run("status", "--porcelain", "--untracked-files=all")
	if err != nil {
		return ""
	}
	repoDir := g.Dir
	if resolved, err := filepath.EvalSymlinks(repoDir); err == nil {
		repoDir = resolved
	}
	ledger := ledgerDir
	if resolved, err := filepath.EvalSymlinks(ledger); err == nil {
		ledger = resolved
	}
	rel, err := filepath.Rel(repoDir, ledger)
	if err != nil || strings.HasPrefix(rel, "..") {
		return out
	}
	rel = filepath.ToSlash(rel)
	var keep []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		path := line
		if len(line) > 3 {
			path = strings.TrimSpace(line[3:])
		}
		if idx := strings.Index(path, " -> "); idx >= 0 {
			path = strings.TrimSpace(path[idx+4:])
		}
		path = strings.Trim(path, `"`)
		if path == rel || strings.HasPrefix(path, rel+"/") {
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

func (l *Ledger) configInfo(opts StartRunOptions) (string, []schema.InputRef, string, error) {
	if opts.Config != "" {
		p, err := l.resolveInputPath("config file", opts.Config)
		if err != nil {
			return "", nil, "", err
		}
		specPath, err := l.specInputPath("config file", opts.Config, p)
		if err != nil {
			return "", nil, "", err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return "", nil, "", Wrap(CodeGeneral, "read config file", err)
		}
		return HashFileBytes(b), []schema.InputRef{{Path: specPath, SHA256: HashFileBytes(b)}}, specPath, nil
	}
	h, err := HashValue(map[string]any{
		"method":  opts.Method,
		"split":   opts.Split,
		"seed":    opts.Seed,
		"command": opts.Command,
		"env":     opts.Env,
	})
	if err != nil {
		return "", nil, "", Wrap(CodeGeneral, "hash config", err)
	}
	return h, nil, "", nil
}

func (l *Ledger) dataInfo(opts StartRunOptions) (*schema.DataRef, *schema.InputRef, error) {
	if opts.Data == "" {
		return nil, nil, nil
	}
	ref := &schema.DataRef{Path: opts.Data, SHA256: opts.DataSHA256}
	if strings.Contains(opts.Data, "://") {
		ref.URI = opts.Data
		if ref.SHA256 == "" {
			return nil, nil, Errorf(CodeUsage, "remote data requires --data-sha256")
		}
	} else {
		p, err := l.resolveInputPath("data file", opts.Data)
		if err != nil {
			return nil, nil, err
		}
		specPath, err := l.specInputPath("data file", opts.Data, p)
		if err != nil {
			return nil, nil, err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, nil, Wrap(CodeGeneral, "read data file", err)
		}
		ref.Path = specPath
		ref.SHA256 = HashFileBytes(b)
		in := &schema.InputRef{Path: specPath, SHA256: ref.SHA256}
		return ref, in, nil
	}
	in := &schema.InputRef{Path: opts.Data, SHA256: ref.SHA256}
	return ref, in, nil
}

// resolveInputPath resolves a local config/data path. Relative paths are looked
// up under local.workdir, then remote.workdir only when it exists locally, then
// the project root. Errors list every local path that was tried.
func (l *Ledger) resolveInputPath(kind, p string) (string, error) {
	if filepath.IsAbs(p) {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return filepath.Clean(p), nil
		}
		return "", Errorf(CodeNotFound, "%s not found: %s", kind, p)
	}
	var tried []string
	for _, base := range l.inputCandidates() {
		candidate := filepath.Clean(filepath.Join(base, p))
		tried = append(tried, candidate)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", Errorf(CodeNotFound, "%s not found; tried %s (set local.workdir or remote.workdir, or use an absolute path)", kind, strings.Join(tried, " and "))
}

// specInputPath keeps remote-facing config/data paths relative. With a
// local.workdir configured, an absolute local path is accepted only inside that
// clone and is recorded as a relative path for the remote runner.
func (l *Ledger) specInputPath(kind, original, resolved string) (string, error) {
	if !filepath.IsAbs(original) {
		clean := filepath.Clean(original)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", Errorf(CodeUsage, "%s path must not escape the workdir: %s", kind, original)
		}
		return filepath.ToSlash(clean), nil
	}
	local := l.LocalWorkdir()
	if local == "" {
		return original, nil
	}
	if !WithinDir(local, resolved) {
		return "", Errorf(CodeUsage, "%s absolute path must be inside local.workdir %s; use a relative path", kind, local)
	}
	rel, err := filepath.Rel(local, resolved)
	if err != nil {
		return "", Wrap(CodeUsage, "relativize "+kind+" path", err)
	}
	return filepath.ToSlash(rel), nil
}

func dataSHA(d *schema.DataRef) string {
	if d == nil {
		return ""
	}
	return d.SHA256
}

func sanitizeRunID(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-.")
	if out == "" {
		return "run"
	}
	return out
}

func buildManifest(l *Ledger, runID string, artifacts []schema.Artifact) (*schema.Manifest, error) {
	runDir := l.RunDir(runID)
	m := &schema.Manifest{
		RunID:     runID,
		Project:   l.Project.Project,
		CreatedAt: l.NowUTC(),
		Files:     []schema.ManifestFile{},
		Artifacts: artifacts,
	}
	var files []string
	err := filepath.WalkDir(runDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name := d.Name()
		if name == "manifest.json" || name == "result.json" || strings.HasPrefix(name, ".tmp-") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, Wrap(CodeGeneral, "scan run directory", err)
	}
	sort.Strings(files)
	for _, p := range files {
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, Wrap(CodeGeneral, "read "+p, err)
		}
		rel, _ := filepath.Rel(runDir, p)
		m.Files = append(m.Files, schema.ManifestFile{
			Path:   filepath.ToSlash(rel),
			SHA256: HashFileBytes(b),
			Size:   int64(len(b)),
		})
	}
	return m, nil
}

func LoadRunResult(l *Ledger, runID string) (*schema.RunResult, error) {
	raw, err := os.ReadFile(filepath.Join(l.RunDir(runID), "result.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, Wrap(CodeGeneral, "read result.json", err)
	}
	var r schema.RunResult
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, Wrap(CodeGeneral, "parse result.json", err)
	}
	return &r, nil
}

func LoadManifest(l *Ledger, runID string) (*schema.Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(l.RunDir(runID), "manifest.json"))
	if err != nil {
		return nil, Wrap(CodeGeneral, "read manifest.json", err)
	}
	var m schema.Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, Wrap(CodeGeneral, "parse manifest.json", err)
	}
	return &m, nil
}

type RunInfo struct {
	RunID      string `json:"run_id"`
	TaskID     string `json:"task_id"`
	Status     string `json:"status"`
	ExitCode   int    `json:"exit_code"`
	SpecPath   string `json:"spec_path"`
	ResultPath string `json:"result_path,omitempty"`
}

func ListRuns(l *Ledger) ([]RunInfo, error) {
	entries, err := os.ReadDir(l.RunsDir())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, Wrap(CodeGeneral, "read runs directory", err)
	}
	out := []RunInfo{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		spec, err := l.LoadRunSpec(e.Name())
		if err != nil {
			continue
		}
		info := RunInfo{RunID: spec.RunID, TaskID: spec.TaskID, Status: "running", SpecPath: filepath.Join(l.RunDir(e.Name()), "run_spec.json")}
		if r, err := LoadRunResult(l, e.Name()); err == nil && r != nil {
			info.Status = r.Status
			info.ExitCode = r.ExitCode
			info.ResultPath = filepath.Join(l.RunDir(e.Name()), "result.json")
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RunID < out[j].RunID })
	return out, nil
}

func resolveExistingPath(root, p string) string {
	if p == "" {
		return ""
	}
	candidates := []string{p}
	if !filepath.IsAbs(p) {
		candidates = append(candidates, ResolvePath(root, p))
		if abs, err := filepath.Abs(p); err == nil {
			candidates = append(candidates, abs)
		}
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return filepath.Clean(c)
		}
	}
	return ""
}
