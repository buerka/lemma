package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/buerka/lemma/internal/core"
	"github.com/buerka/lemma/internal/schema"
)

func runCLI(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	code, out, errOut := runCLI(args...)
	if code != 0 {
		t.Fatalf("command %v exited %d\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
	}
	return out
}

func decodeJSON[T any](t *testing.T, s string) T {
	t.Helper()
	var v T
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("invalid json: %v\n%s", err, s)
	}
	return v
}

func newProjectRoot(t *testing.T, name string) string {
	t.Helper()
	root := t.TempDir()
	resolved, err := filepath.EvalSymlinks(root)
	if err == nil {
		root = resolved
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte("progress/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("# "+name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "-b", "main")
	runGit(t, root, "add", ".")
	runGit(t, root, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "init")
	return root
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func initDemo(t *testing.T, root, name string) {
	t.Helper()
	code, out, errOut := runCLI("--json", "init", "--root", root, "--project", name,
		"--host", "gpu", "--workdir", "~/project", "--run-root", "~/.lem-runs")
	if code != 0 {
		t.Fatalf("init failed: %d\n%s\n%s", code, out, errOut)
	}
}

func TestEndToEndLedgerFlow(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "demo")
	initDemo(t, root, "demo")

	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1",
		"--title", "baseline reproduction", "--phase", "P0",
		"--acceptance", "metrics.json exists", "--acceptance", "no leakage")

	start := decodeJSON[map[string]any](t, mustRun(t, "--json", "--project", root,
		"run", "start", "--task", "T1", "--method", "baseline", "--split", "random", "--seed", "0",
		"--command", "echo hello"))
	runID := "T1.baseline.random.seed0"
	runDir := filepath.Join(root, "progress", "runs", runID)
	if _, err := os.Stat(filepath.Join(runDir, "run_spec.json")); err != nil {
		t.Fatal("run_spec.json missing")
	}
	if _, err := os.Stat(filepath.Join(runDir, "REMOTE_TASK.md")); err != nil {
		t.Fatal("REMOTE_TASK.md missing")
	}
	specRaw, err := os.ReadFile(filepath.Join(runDir, "run_spec.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec schema.RunSpec
	if err := json.Unmarshal(specRaw, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.Code.Commit == "" {
		t.Fatalf("run spec did not record code commit: %+v", spec.Code)
	}
	if spec.Code.Dirty {
		t.Fatalf("run spec marked a clean tree dirty: %+v", spec.Code)
	}
	if spec.ConfigHash == "" || !strings.HasPrefix(spec.ConfigHash, "sha256:") {
		t.Fatalf("missing config hash: %q", spec.ConfigHash)
	}
	if start["run_spec"] == nil {
		t.Fatal("run start json missing run_spec")
	}

	remoteResult := map[string]any{
		"run_id": runID, "status": "succeeded", "exit_code": 0,
		"metrics": map[string]any{"macro_f2": 0.89},
		"artifacts": []map[string]any{{
			"name": "checkpoint", "uri": "ssh://gpu/~/.lem-runs/" + runID + "/checkpoint.pt",
			"sha256": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		}},
		"logs_tail": "done",
	}
	remoteBytes, _ := json.Marshal(remoteResult)
	remotePath := filepath.Join(runDir, "remote_result.json")
	if err := os.WriteFile(remotePath, remoteBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "--json", "--project", root, "run", "finish", runID, "--result", remotePath)
	if _, err := os.Stat(filepath.Join(runDir, "metrics.json")); err != nil {
		t.Fatal("metrics.json missing")
	}
	if _, err := os.Stat(filepath.Join(runDir, "manifest.json")); err != nil {
		t.Fatal("manifest.json missing")
	}

	csPath := filepath.Join(root, "progress", "changesets", "cs-e2e.json")
	planOut := decodeJSON[map[string]any](t, mustRun(t, "--json", "--project", root,
		"plan", "--task", "T1", "--status", "done",
		"--evidence", "progress/runs/"+runID+"/metrics.json", "--out", csPath))
	cs := planOut["changeset"].(map[string]any)
	csHash := cs["hash"].(string)
	if !strings.HasPrefix(csHash, "sha256:") {
		t.Fatalf("bad changeset hash %q", csHash)
	}
	mustRun(t, "--json", "--project", root, "review", csPath)
	if _, err := os.Stat(filepath.Join(root, "progress", "REVIEW.md")); err != nil {
		t.Fatal("REVIEW.md missing")
	}
	approveOut := decodeJSON[map[string]any](t, mustRun(t, "--json", "--project", root,
		"approve", csPath, "--by", "human", "--gate", "G4"))
	gate := approveOut["gate"].(map[string]any)
	if gate["changeset_hash"] != csHash {
		t.Fatalf("gate hash = %v, want %s", gate["changeset_hash"], csHash)
	}
	approvedFiles := runGit(t, filepath.Join(root, "progress"), "show", "--pretty=format:", "--name-only", "HEAD")
	if strings.TrimSpace(approvedFiles) != "gates.jsonl" {
		t.Fatalf("approve commit touched more than gates.jsonl: %q", approvedFiles)
	}

	applyOut := decodeJSON[core.ApplyResult](t, mustRun(t, "--json", "--project", root, "apply", csPath))
	if applyOut.Receipt.ChangesetHash != csHash || applyOut.Receipt.GitCommit == "" {
		t.Fatalf("bad receipt: %+v", applyOut.Receipt)
	}

	code, duplicateOut, _ := runCLI("--json", "--project", root, "apply", csPath)
	if code != 5 {
		t.Fatalf("duplicate apply exit = %d, want 5\n%s", code, duplicateOut)
	}
	dup := decodeJSON[core.ApplyResult](t, duplicateOut)
	if !dup.AlreadyApplied {
		t.Fatal("duplicate apply did not return the existing receipt")
	}

	verify := decodeJSON[core.VerifyReport](t, mustRun(t, "--json", "--project", root, "verify"))
	if !verify.OK {
		t.Fatalf("verify failed: %+v", verify.Issues)
	}
	if verify.ReceiptCount != 1 || verify.GateCount != 1 {
		t.Fatalf("unexpected verify counts: %+v", verify)
	}
	artifactReport := decodeJSON[core.ArtifactReport](t, mustRun(t, "--json", "--project", root, "artifact", "verify"))
	if !artifactReport.OK {
		t.Fatalf("artifact verify failed: %+v", artifactReport.Checks)
	}

	tasks := decodeJSON[[]schema.Task](t, mustRun(t, "--json", "--project", root, "task", "list"))
	if len(tasks) != 1 || tasks[0].Status != schema.StatusDone {
		t.Fatalf("task not done: %+v", tasks)
	}
	status := decodeJSON[core.Summary](t, mustRun(t, "--json", "--project", root, "status"))
	if status.Revision != applyOut.Receipt.Revision {
		t.Fatalf("status revision %s != receipt revision %s", status.Revision, applyOut.Receipt.Revision)
	}
	logs := decodeJSON[[]schema.Event](t, mustRun(t, "--json", "--project", root,
		"log", "--type", "changeset.apply"))
	if len(logs) != 1 || logs[0].ChangesetID != "cs-e2e" {
		t.Fatalf("unexpected apply log: %+v", logs)
	}

	if dirty := runGit(t, filepath.Join(root, "progress"), "status", "--porcelain"); dirty != "" {
		t.Fatalf("progress git is dirty:\n%s", dirty)
	}
	logOut := runGit(t, filepath.Join(root, "progress"), "log", "--oneline")
	if lines := strings.Split(logOut, "\n"); len(lines) < 8 {
		t.Fatalf("expected a logical commit history, got:\n%s", logOut)
	}
	if _, err := os.Stat(filepath.Join(root, "progress", "receipts", "cs-e2e.json")); err != nil {
		t.Fatal("receipt missing")
	}
}

func TestConcurrentApplyOnlyOneWins(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "concurrent")
	initDemo(t, root, "concurrent")

	const n = 10
	for i := 0; i < n; i++ {
		mustRun(t, "--json", "--project", root, "task", "add",
			"--id", fmt.Sprintf("T%d", i+1), "--title", fmt.Sprintf("task %d", i+1), "--phase", "P0")
	}
	var paths []string
	for i := 0; i < n; i++ {
		p := filepath.Join(root, "progress", "changesets", fmt.Sprintf("cs-conc-%02d.json", i+1))
		mustRun(t, "--json", "--project", root, "plan",
			"--task", fmt.Sprintf("T%d", i+1), "--status", "doing", "--out", p)
		mustRun(t, "--json", "--project", root, "approve", p, "--by", "human")
		paths = append(paths, p)
	}

	codes := make([]int, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, _, _ := runCLI("--project", root, "apply", paths[i], "--lock-timeout", "10s")
			codes[i] = code
		}(i)
	}
	wg.Wait()

	success, conflicts, busy := 0, 0, 0
	for _, code := range codes {
		switch code {
		case 0:
			success++
		case 3:
			conflicts++
		case 4:
			busy++
		default:
			t.Fatalf("unexpected apply exit code %d (all: %v)", code, codes)
		}
	}
	if success != 1 {
		t.Fatalf("successful applies = %d, want 1 (all: %v)", success, codes)
	}
	if conflicts+busy != n-1 {
		t.Fatalf("conflicts=%d busy=%d, want %d", conflicts, busy, n-1)
	}

	report := decodeJSON[core.VerifyReport](t, mustRun(t, "--json", "--project", root, "verify"))
	if !report.OK {
		t.Fatalf("verify after concurrent apply: %+v", report.Issues)
	}
	if report.ReceiptCount != 1 {
		t.Fatalf("receipt count = %d, want 1", report.ReceiptCount)
	}
}

func TestMultiProjectIsolation(t *testing.T) {
	configDir := t.TempDir()
	t.Setenv("LEM_CONFIG_DIR", configDir)
	t.Setenv("LEM_FAILPOINT", "")
	t.Setenv("LEM_PROJECT", "")

	rootA := newProjectRoot(t, "alpha")
	rootB := newProjectRoot(t, "beta")
	initDemo(t, rootA, "alpha")
	initDemo(t, rootB, "beta")
	mustRun(t, "--json", "--project", rootA, "task", "add", "--id", "TA", "--title", "alpha task")
	mustRun(t, "--json", "--project", rootB, "task", "add", "--id", "TB", "--title", "beta task")
	mustRun(t, "--json", "project", "add", "alpha", "--root", rootA, "--default")
	mustRun(t, "--json", "project", "add", "beta", "--root", rootB)

	alphaBefore := decodeJSON[core.Summary](t, mustRun(t, "--json", "--project", "alpha", "status"))
	betaBefore := decodeJSON[core.Summary](t, mustRun(t, "--json", "--project", "beta", "status"))
	if alphaBefore.Project != "alpha" || betaBefore.Project != "beta" {
		t.Fatalf("registry resolution mixed projects: %q %q", alphaBefore.Project, betaBefore.Project)
	}

	var wg sync.WaitGroup
	errs := make(chan string, 40)
	for i := 0; i < 20; i++ {
		for _, id := range []string{"alpha", "beta"} {
			wg.Add(1)
			go func(id string) {
				defer wg.Done()
				code, out, errOut := runCLI("--json", "--project", id, "status")
				if code != 0 {
					errs <- fmt.Sprintf("%s status: %d %s", id, code, errOut)
					return
				}
				var s core.Summary
				if err := json.Unmarshal([]byte(out), &s); err != nil {
					errs <- err.Error()
					return
				}
				if s.Project != id {
					errs <- fmt.Sprintf("project %s returned %s", id, s.Project)
				}
			}(id)
		}
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Fatal(e)
	}

	csPath := filepath.Join(rootA, "progress", "changesets", "cs-alpha.json")
	mustRun(t, "--json", "--project", "alpha", "plan", "--task", "TA", "--status", "doing", "--out", csPath)
	mustRun(t, "--json", "--project", "alpha", "approve", csPath, "--by", "human")
	mustRun(t, "--json", "--project", "alpha", "apply", csPath)

	alphaAfter := decodeJSON[core.Summary](t, mustRun(t, "--json", "--project", "alpha", "status"))
	betaAfter := decodeJSON[core.Summary](t, mustRun(t, "--json", "--project", "beta", "status"))
	if alphaAfter.Tasks[0].Status != "doing" {
		t.Fatalf("alpha task not updated: %+v", alphaAfter.Tasks)
	}
	if betaAfter.Tasks[0].Status != "todo" {
		t.Fatalf("beta task leaked state: %+v", betaAfter.Tasks)
	}
	if alphaAfter.Revision == betaAfter.Revision {
		t.Fatal("projects unexpectedly share a revision")
	}

	t.Setenv("LEM_PROJECT", "beta")
	next := decodeJSON[map[string]any](t, mustRun(t, "--json", "next"))
	task := next["task"].(map[string]any)
	if task["id"] != "TB" {
		t.Fatalf("LEM_PROJECT resolution returned %v", task["id"])
	}
	t.Setenv("LEM_PROJECT", "")

	cwd, _ := os.Getwd()
	defer func() { _ = os.Chdir(cwd) }()
	if err := os.Chdir(filepath.Join(rootA, "progress", "runs")); err != nil {
		t.Fatal(err)
	}
	discovered := decodeJSON[core.Summary](t, mustRun(t, "--json", "status"))
	if discovered.Project != "alpha" {
		t.Fatalf("cwd discovery returned %q", discovered.Project)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatal(err)
	}

	list := decodeJSON[map[string]any](t, mustRun(t, "--json", "project", "list"))
	if list["default"] != "alpha" {
		t.Fatalf("registry default = %v", list["default"])
	}
	if projects := list["projects"].([]any); len(projects) != 2 {
		t.Fatalf("registry projects = %v", projects)
	}
	mustRun(t, "--json", "project", "remove", "beta")
	list = decodeJSON[map[string]any](t, mustRun(t, "--json", "project", "list"))
	if projects := list["projects"].([]any); len(projects) != 1 {
		t.Fatalf("project remove failed: %v", projects)
	}
}

func TestApplyCrashRollbackAndBusy(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "crash")
	initDemo(t, root, "crash")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	csPath := filepath.Join(root, "progress", "changesets", "cs-crash.json")
	mustRun(t, "--json", "--project", root, "plan", "--task", "T1", "--status", "doing", "--out", csPath)
	mustRun(t, "--json", "--project", root, "approve", csPath, "--by", "human")

	l, err := core.Open(root, "progress")
	if err != nil {
		t.Fatal(err)
	}
	held, err := core.AcquireLock(l.Dir, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	code, _, errOut := runCLI("--project", root, "apply", csPath, "--lock-timeout", "100ms")
	if code != 4 {
		_ = held.Release()
		t.Fatalf("busy apply exit = %d, want 4 (%s)", code, errOut)
	}
	_ = held.Release()

	t.Setenv("LEM_FAILPOINT", "apply-after-task-write")
	code, out, errOut := runCLI("--project", root, "apply", csPath)
	if code != 1 || !strings.Contains(out+errOut, "injected failure") {
		t.Fatalf("injected apply exit=%d out=%s err=%s", code, out, errOut)
	}
	t.Setenv("LEM_FAILPOINT", "")

	verify := decodeJSON[core.VerifyReport](t, mustRun(t, "--json", "--project", root, "verify"))
	if !verify.OK {
		t.Fatalf("verify after crash rollback: %+v", verify.Issues)
	}
	tasks := decodeJSON[[]schema.Task](t, mustRun(t, "--json", "--project", root, "task", "list"))
	if tasks[0].Status != schema.StatusTodo {
		t.Fatalf("task changed after rollback: %+v", tasks)
	}
	mustRun(t, "--json", "--project", root, "status")
	if temps := findTempFiles(t, filepath.Join(root, "progress")); len(temps) != 0 {
		t.Fatalf("stale temp files: %v", temps)
	}
	mustRun(t, "--json", "--project", root, "apply", csPath)
	verify = decodeJSON[core.VerifyReport](t, mustRun(t, "--json", "--project", root, "verify"))
	if !verify.OK {
		t.Fatalf("verify after recovery: %+v", verify.Issues)
	}
}

func findTempFiles(t *testing.T, dir string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
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
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestExitCodesAndStaleSchema(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	root := newProjectRoot(t, "codes")
	initDemo(t, root, "codes")

	code, out, _ := runCLI("--json", "bogus")
	if code != 2 {
		t.Fatalf("unknown command exit = %d, want 2", code)
	}
	var errPayload map[string]any
	if err := json.Unmarshal([]byte(out), &errPayload); err != nil {
		t.Fatalf("usage error was not JSON on stdout: %v\n%s", err, out)
	}
	if errPayload["ok"] != false {
		t.Fatalf("unexpected usage error payload: %v", errPayload)
	}

	code, _, _ = runCLI("--project", root, "task", "add", "--id", "TX", "--title", "x",
		"--expected-revision", "sha256:0000000000000000000000000000000000000000000000000000000000000000")
	if code != 3 {
		t.Fatalf("stale expected_revision exit = %d, want 3", code)
	}

	projectYAML := filepath.Join(root, "progress", "project.yaml")
	raw, err := os.ReadFile(projectYAML)
	if err != nil {
		t.Fatal(err)
	}
	raw = bytes.Replace(raw, []byte("schema_version: 1"), []byte("schema_version: 99"), 1)
	if err := os.WriteFile(projectYAML, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ = runCLI("--json", "--project", root, "status")
	if code != 6 {
		t.Fatalf("stale schema exit = %d, want 6; out=%s", code, out)
	}
	var stale map[string]any
	if err := json.Unmarshal([]byte(out), &stale); err != nil {
		t.Fatal(err)
	}
	errObj := stale["error"].(map[string]any)
	if errObj["code"] != "STALE" {
		t.Fatalf("stale error code = %v", errObj["code"])
	}
}

func TestDoctorWithSSHHost(t *testing.T) {
	configDir := t.TempDir()
	home := t.TempDir()
	t.Setenv("LEM_CONFIG_DIR", configDir)
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host gpu\n  HostName 10.0.0.10\n  User alice\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := newProjectRoot(t, "doctor")
	initDemo(t, root, "doctor")
	out := mustRun(t, "--json", "--project", root, "doctor")
	var report struct {
		OK     bool          `json:"ok"`
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal([]byte(out), &report); err != nil {
		t.Fatal(err)
	}
	if !report.OK {
		t.Fatalf("doctor failed: %+v", report.Checks)
	}
	found := false
	for _, chk := range report.Checks {
		if chk.Name == "ssh host" && chk.Status == "ok" && strings.Contains(chk.Detail, "gpu") {
			found = true
		}
	}
	if !found {
		t.Fatalf("doctor did not validate SSH host: %+v", report.Checks)
	}
}
