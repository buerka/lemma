package cli

import (
	"bufio"
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

const testApprovalToken = "test-approval-token"

func ensureApprovalToken(t *testing.T) {
	t.Helper()
	if os.Getenv("LEM_CONFIG_DIR") == "" {
		t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	}
	dir := core.ConfigDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "approval-token"), []byte(testApprovalToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustApprove(t *testing.T, project, ref string, extra ...string) string {
	t.Helper()
	ensureApprovalToken(t)
	args := []string{"--json", "--project", project, "approve", ref, "--by", "human", "--token", testApprovalToken}
	args = append(args, extra...)
	return mustRun(t, args...)
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
	approveOut := decodeJSON[map[string]any](t, mustApprove(t, root, csPath, "--gate", "G4"))
	gate := approveOut["gate"].(map[string]any)
	if gate["changeset_hash"] != csHash {
		t.Fatalf("gate hash = %v, want %s", gate["changeset_hash"], csHash)
	}
	if gate["approval_method"] != "token" {
		t.Fatalf("approval_method = %v, want token", gate["approval_method"])
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
	code, duplicateHuman, _ := runCLI("--project", root, "apply", csPath)
	if code != 5 {
		t.Fatalf("duplicate human apply exit = %d, want 5", code)
	}
	if strings.Count(duplicateHuman, "applied") != 1 || !strings.Contains(duplicateHuman, "receipt:") {
		t.Fatalf("duplicate apply output is not a single line:\n%s", duplicateHuman)
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
	defaultArtifacts := decodeJSON[[]core.ArtifactEntry](t, mustRun(t, "--json", "--project", root, "artifact", "list"))
	if len(defaultArtifacts) != 1 || defaultArtifacts[0].Kind != "artifact" {
		t.Fatalf("artifact list default is noisy: %+v", defaultArtifacts)
	}
	allArtifacts := decodeJSON[[]core.ArtifactEntry](t, mustRun(t, "--json", "--project", root, "artifact", "list", "--all"))
	hasRunfile := false
	for _, entry := range allArtifacts {
		if entry.Kind == "runfile" {
			hasRunfile = true
		}
	}
	if !hasRunfile {
		t.Fatalf("artifact list --all did not include runfiles: %+v", allArtifacts)
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
		mustApprove(t, root, p)
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
	mustApprove(t, "alpha", csPath)
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
	mustApprove(t, root, csPath)

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

func TestVerifyDetectsTamperedEvent(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "tamper")
	initDemo(t, root, "tamper")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")

	eventsPath := filepath.Join(root, "progress", "events.jsonl")
	original, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatal(err)
	}
	tampered := bytes.Replace(original, []byte(`"title":"task"`), []byte(`"title":"tampered"`), 1)
	if bytes.Equal(tampered, original) {
		t.Fatal("test could not find a payload to tamper with")
	}
	if err := os.WriteFile(eventsPath, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI("--json", "--project", root, "verify")
	if code != 1 {
		t.Fatalf("tampered verify exit = %d, want 1\n%s", code, out)
	}
	report := decodeJSON[core.VerifyReport](t, out)
	if report.OK {
		t.Fatal("tampered event chain was accepted")
	}
	found := false
	for _, issue := range report.Issues {
		if issue.Code == "EVENT_HASH" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing EVENT_HASH issue: %+v", report.Issues)
	}

	if err := os.WriteFile(eventsPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	report = decodeJSON[core.VerifyReport](t, mustRun(t, "--json", "--project", root, "verify"))
	if !report.OK {
		t.Fatalf("verify after restore: %+v", report.Issues)
	}
}

func TestArtifactVerifyDetectsCorruption(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "artifact")
	initDemo(t, root, "artifact")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1",
		"--method", "baseline", "--split", "random", "--seed", "0", "--command", "true")
	runDir := filepath.Join(root, "progress", "runs", "T1.baseline.random.seed0")
	remote := []byte(`{"run_id":"T1.baseline.random.seed0","status":"succeeded","exit_code":0,"metrics":{"score":1}}`)
	if err := os.WriteFile(filepath.Join(runDir, "remote_result.json"), remote, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "--json", "--project", root, "run", "finish", "T1.baseline.random.seed0",
		"--result", filepath.Join(runDir, "remote_result.json"))

	metricsPath := filepath.Join(runDir, "metrics.json")
	original, err := os.ReadFile(metricsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metricsPath, append(original, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, _ := runCLI("--json", "--project", root, "artifact", "verify")
	if code != 0 {
		t.Fatalf("default verify should ignore runfiles, exit = %d\n%s", code, out)
	}
	report := decodeJSON[core.ArtifactReport](t, out)
	if !report.OK || report.Checked != 0 {
		t.Fatalf("default artifact verify checked runfiles: %+v", report)
	}
	code, out, _ = runCLI("--json", "--project", root, "artifact", "verify", "--all")
	if code != 1 {
		t.Fatalf("corrupt runfile with --all exit = %d, want 1\n%s", code, out)
	}
	report = decodeJSON[core.ArtifactReport](t, out)
	if report.OK || report.Failed == 0 {
		t.Fatalf("corrupt runfile was accepted by --all: %+v", report)
	}
	if err := os.WriteFile(metricsPath, original, 0o644); err != nil {
		t.Fatal(err)
	}
	report = decodeJSON[core.ArtifactReport](t, mustRun(t, "--json", "--project", root, "artifact", "verify", "--all"))
	if !report.OK {
		t.Fatalf("artifact verify --all after restore: %+v", report.Checks)
	}
}

func TestRunFinishCopiesExternalResult(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "external")
	initDemo(t, root, "external")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1",
		"--method", "baseline", "--split", "random", "--seed", "0", "--command", "true")
	external := filepath.Join(root, "external_result.json")
	body := []byte(`{"run_id":"T1.baseline.random.seed0","status":"succeeded","exit_code":0,"metrics":{"score":2}}`)
	if err := os.WriteFile(external, body, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "--json", "--project", root, "run", "finish", "T1.baseline.random.seed0", "--result", external)
	runDir := filepath.Join(root, "progress", "runs", "T1.baseline.random.seed0")
	if _, err := os.Stat(filepath.Join(runDir, "remote_result.json")); err != nil {
		t.Fatal("external remote result was not copied into the run directory")
	}
	manifest := decodeJSON[schema.Manifest](t, mustReadFile(t, filepath.Join(runDir, "manifest.json")))
	found := false
	for _, f := range manifest.Files {
		if f.Path == "remote_result.json" {
			found = true
		}
	}
	if !found {
		t.Fatalf("manifest does not include remote_result.json: %+v", manifest.Files)
	}
	report := decodeJSON[core.VerifyReport](t, mustRun(t, "--json", "--project", root, "verify"))
	if !report.OK {
		t.Fatalf("verify after external result: %+v", report.Issues)
	}
}

func TestRunStartIdempotentAndForce(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "restart")
	initDemo(t, root, "restart")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1",
		"--method", "baseline", "--split", "random", "--seed", "0", "--command", "true")
	code, out, _ := runCLI("--json", "--project", root, "run", "start", "--task", "T1",
		"--method", "baseline", "--split", "random", "--seed", "0", "--command", "true")
	if code != 5 {
		t.Fatalf("duplicate run start exit = %d, want 5\n%s", code, out)
	}
	var start struct {
		AlreadyExists bool `json:"already_exists"`
	}
	if err := json.Unmarshal([]byte(out), &start); err != nil || !start.AlreadyExists {
		t.Fatalf("unexpected duplicate start payload: %v %s", err, out)
	}

	runDir := filepath.Join(root, "progress", "runs", "T1.baseline.random.seed0")
	remote := []byte(`{"run_id":"T1.baseline.random.seed0","status":"succeeded","exit_code":0}`)
	if err := os.WriteFile(filepath.Join(runDir, "remote_result.json"), remote, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "--json", "--project", root, "run", "finish", "T1.baseline.random.seed0",
		"--result", filepath.Join(runDir, "remote_result.json"))
	if _, err := os.Stat(filepath.Join(runDir, "result.json")); err != nil {
		t.Fatal("result.json missing after finish")
	}
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1",
		"--method", "baseline", "--split", "random", "--seed", "0", "--command", "true", "--force")
	if _, err := os.Stat(filepath.Join(runDir, "result.json")); !os.IsNotExist(err) {
		t.Fatal("forced restart left the previous result.json behind")
	}
	report := decodeJSON[core.VerifyReport](t, mustRun(t, "--json", "--project", root, "verify"))
	if !report.OK {
		t.Fatalf("verify after forced restart: %+v", report.Issues)
	}
}

func TestPlanRequiresEvidence(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	root := newProjectRoot(t, "plan")
	initDemo(t, root, "plan")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	code, _, _ := runCLI("--project", root, "plan", "--task", "T1", "--status", "done",
		"--out", filepath.Join(root, "progress", "changesets", "cs-no-evidence.json"))
	if code != 2 {
		t.Fatalf("plan without evidence exit = %d, want 2", code)
	}
	code, _, _ = runCLI("--project", root, "plan", "--task", "T1", "--status", "done",
		"--evidence", "missing.json", "--out", filepath.Join(root, "progress", "changesets", "cs-missing.json"))
	if code != 1 {
		t.Fatalf("plan with missing evidence exit = %d, want 1", code)
	}
}

func TestProjectRegistryConflictAndForce(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	rootA := newProjectRoot(t, "alpha")
	rootB := newProjectRoot(t, "beta")
	initDemo(t, rootA, "alpha")
	initDemo(t, rootB, "beta")
	mustRun(t, "--json", "project", "add", "shared", "--root", rootA)
	code, _, _ := runCLI("--json", "project", "add", "shared", "--root", rootB)
	if code != 3 {
		t.Fatalf("registry conflict exit = %d, want 3", code)
	}
	mustRun(t, "--json", "project", "add", "shared", "--root", rootB, "--force")
	out := mustRun(t, "--json", "project", "list")
	list := decodeJSON[map[string]any](t, out)
	projects := list["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("unexpected registry: %s", out)
	}
	entry := projects[0].(map[string]any)
	if entry["root"] != rootB {
		t.Fatalf("force add did not replace root: %v", entry)
	}
}

func TestCrossProcessLockReturnsBusy(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "crossproc")
	initDemo(t, root, "crossproc")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	csPath := filepath.Join(root, "progress", "changesets", "cs-lock.json")
	mustRun(t, "--json", "--project", root, "plan", "--task", "T1", "--status", "doing", "--out", csPath)
	mustApprove(t, root, csPath)

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperHoldLock$")
	cmd.Env = append(os.Environ(), "LEM_HELPER_HOLD=1", "LEM_HELPER_ROOT="+root)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	cleanup := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	defer cleanup()

	ready := make(chan bool, 1)
	go func() {
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "LOCKED") {
				ready <- true
				return
			}
		}
		ready <- false
	}()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatalf("helper did not acquire the lock: %s", stderr.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("helper did not report lock acquisition: %s", stderr.String())
	}

	code, _, errOut := runCLI("--project", root, "apply", csPath, "--lock-timeout", "200ms")
	if code != 4 {
		t.Fatalf("cross-process apply exit = %d, want 4 (%s)", code, errOut)
	}
}

// TestHelperHoldLock is a helper process used by TestCrossProcessLockReturnsBusy.
func TestHelperHoldLock(t *testing.T) {
	if os.Getenv("LEM_HELPER_HOLD") != "1" {
		return
	}
	root := os.Getenv("LEM_HELPER_ROOT")
	l, err := core.Open(root, "progress")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	lock, err := core.AcquireLock(l.Dir, 5*time.Second)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	defer func() { _ = lock.Release() }()
	fmt.Println("LOCKED")
	time.Sleep(30 * time.Second)
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func newGitWorkdir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("# workdir\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "init", "-b", "main")
	runGit(t, dir, "add", ".")
	runGit(t, dir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "init")
	return dir
}

func TestRemoteWorkdirContract(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := t.TempDir()
	workdir := newGitWorkdir(t)
	code, out, errOut := runCLI("--json", "init", "--root", root, "--project", "workdir-demo",
		"--host", "local", "--workdir", workdir, "--run-root", "~/.lem-runs")
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errOut)
	}
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")

	configBytes := []byte("seed: 0\n")
	dataBytes := []byte("dataset")
	if err := os.MkdirAll(filepath.Join(workdir, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(workdir, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "configs", "train.yaml"), configBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "data", "input.bin"), dataBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workdir, "add", "configs", "data")
	runGit(t, workdir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "inputs")

	start := decodeJSON[core.StartRunResult](t, mustRun(t, "--json", "--project", root,
		"run", "start", "--task", "T1", "--method", "baseline", "--split", "random", "--seed", "0",
		"--command", "true", "--config", "configs/train.yaml", "--data", "data/input.bin"))
	spec := start.Spec
	if spec.Code.Commit == "" || spec.Code.Repo == "" {
		t.Fatalf("code info did not come from remote.workdir: %+v", spec.Code)
	}
	if spec.Code.Dirty {
		t.Fatalf("clean workdir reported dirty: %+v", spec.Code)
	}
	if spec.ConfigHash != core.HashFileBytes(configBytes) {
		t.Fatalf("config hash = %s, want %s", spec.ConfigHash, core.HashFileBytes(configBytes))
	}
	if spec.Data == nil || spec.Data.SHA256 != core.HashFileBytes(dataBytes) {
		t.Fatalf("data hash not resolved from workdir: %+v", spec.Data)
	}
	if spec.ConfigPath != "configs/train.yaml" || spec.Data.Path != "data/input.bin" {
		t.Fatalf("unexpected spec input paths: config=%q data=%+v", spec.ConfigPath, spec.Data)
	}

	code, _, errOut = runCLI("--project", root, "run", "start", "--task", "T1",
		"--run-id", "missing-config", "--config", "nope.yaml")
	if code != 1 {
		t.Fatalf("missing config exit = %d, want 1 (%s)", code, errOut)
	}
	if !strings.Contains(errOut, workdir) || !strings.Contains(errOut, root) {
		t.Fatalf("missing config error does not list both roots: %s", errOut)
	}
	code, _, errOut = runCLI("--project", root, "run", "start", "--task", "T1",
		"--run-id", "missing-data", "--data", "nope.bin")
	if code != 1 {
		t.Fatalf("missing data exit = %d, want 1 (%s)", code, errOut)
	}
	if !strings.Contains(errOut, workdir) || !strings.Contains(errOut, root) {
		t.Fatalf("missing data error does not list both roots: %s", errOut)
	}
}

func TestSymlinkRootCleanRepoNotDirty(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	actual := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(actual); err == nil {
		actual = resolved
	}
	if err := os.WriteFile(filepath.Join(actual, "README.md"), []byte("# repo\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, actual, "init", "-b", "main")
	runGit(t, actual, "add", ".")
	runGit(t, actual, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "init")
	link := filepath.Join(t.TempDir(), "root-link")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}

	code, out, errOut := runCLI("--json", "init", "--root", link, "--project", "symlink",
		"--host", "local", "--workdir", link, "--run-root", "~/.lem-runs")
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errOut)
	}
	mustRun(t, "--json", "--project", link, "task", "add", "--id", "T1", "--title", "task")
	start := decodeJSON[core.StartRunResult](t, mustRun(t, "--json", "--project", link,
		"run", "start", "--task", "T1", "--command", "true"))
	if start.Spec.Code.Commit == "" {
		t.Fatalf("missing code commit: %+v", start.Spec.Code)
	}
	if start.Spec.Code.Dirty {
		t.Fatalf("symlinked clean repo reported dirty: %+v", start.Spec.Code)
	}
}

func TestDirectDoneTransitionRejected(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	root := newProjectRoot(t, "done-boundary")
	initDemo(t, root, "done-boundary")

	code, _, _ := runCLI("--project", root, "task", "add", "--id", "T2", "--title", "direct done", "--status", "done")
	if code != 2 {
		t.Fatalf("task add --status done exit = %d, want 2", code)
	}
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	code, _, _ = runCLI("--project", root, "task", "update", "T1", "--status", "done")
	if code != 2 {
		t.Fatalf("task update --status done exit = %d, want 2", code)
	}
	tasks := decodeJSON[[]schema.Task](t, mustRun(t, "--json", "--project", root, "task", "list"))
	if len(tasks) != 1 || tasks[0].Status != schema.StatusTodo {
		t.Fatalf("direct update changed task state: %+v", tasks)
	}

	evidence := filepath.Join(root, "evidence.json")
	if err := os.WriteFile(evidence, []byte(`{"ok":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	csPath := filepath.Join(root, "progress", "changesets", "cs-done.json")
	mustRun(t, "--json", "--project", root, "plan", "--task", "T1", "--status", "done",
		"--evidence", evidence, "--out", csPath)
	if _, err := os.Stat(csPath); err != nil {
		t.Fatal("plan/approval path for done no longer works")
	}
}

func TestApprovalGateSecurity(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "approval")
	initDemo(t, root, "approval")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task1")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T2", "--title", "task2")
	cs1 := filepath.Join(root, "progress", "changesets", "cs-approval-1.json")
	cs2 := filepath.Join(root, "progress", "changesets", "cs-approval-2.json")
	mustRun(t, "--json", "--project", root, "plan", "--task", "T1", "--status", "doing", "--out", cs1)
	mustRun(t, "--json", "--project", root, "plan", "--task", "T2", "--status", "doing", "--out", cs2)

	runApprove := func(csPath string, stdin string, isTTY bool, token string) (int, string, string) {
		args := []string{"--json", "--project", root, "approve", csPath, "--by", "human"}
		if token != "" {
			args = append(args, "--token", token)
		}
		var out, errOut bytes.Buffer
		code := RunWithIO(args, strings.NewReader(stdin), &out, &errOut, isTTY)
		return code, out.String(), errOut.String()
	}

	gatesPath := filepath.Join(root, "progress", "gates.jsonl")
	countGates := func() int {
		raw, err := os.ReadFile(gatesPath)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			if strings.TrimSpace(line) != "" {
				n++
			}
		}
		return n
	}

	code, _, errOut := runApprove(cs1, "", false, "")
	if code != 1 {
		t.Fatalf("non-TTY approval without token exit = %d, want 1 (%s)", code, errOut)
	}
	if countGates() != 0 {
		t.Fatal("rejected approval wrote a gate")
	}

	tokenPath := filepath.Join(core.ConfigDir(), "approval-token")
	if err := os.MkdirAll(filepath.Dir(tokenPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tokenPath, []byte(testApprovalToken), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tokenPath, 0o644); err != nil {
		t.Fatal(err)
	}
	code, out, errOut := runApprove(cs1, "", false, testApprovalToken)
	if code != 1 || !strings.Contains(out+errOut, "chmod 600") {
		t.Fatalf("insecure token file exit=%d out=%s err=%s", code, out, errOut)
	}

	if err := os.Chmod(tokenPath, 0o600); err != nil {
		t.Fatal(err)
	}
	code, out, errOut = runApprove(cs1, "", false, testApprovalToken)
	if code != 0 {
		t.Fatalf("token approval exit=%d out=%s err=%s", code, out, errOut)
	}
	var tokenResult struct {
		Gate schema.Gate `json:"gate"`
	}
	if err := json.Unmarshal([]byte(out), &tokenResult); err != nil {
		t.Fatal(err)
	}
	if tokenResult.Gate.ApprovalMethod != "token" {
		t.Fatalf("token gate method = %q", tokenResult.Gate.ApprovalMethod)
	}

	raw, err := os.ReadFile(cs2)
	if err != nil {
		t.Fatal(err)
	}
	var cs schema.Changeset
	if err := json.Unmarshal(raw, &cs); err != nil {
		t.Fatal(err)
	}
	hash, err := core.ComputeChangesetHash(cs)
	if err != nil {
		t.Fatal(err)
	}
	short := strings.TrimPrefix(hash, "sha256:")[:12]
	code, _, _ = runApprove(cs2, "wrong\n", true, "")
	if code != 1 {
		t.Fatalf("wrong TTY confirmation exit = %d, want 1", code)
	}
	if countGates() != 1 {
		t.Fatal("wrong TTY confirmation wrote a gate")
	}
	code, out, errOut = runApprove(cs2, short+"\n", true, "")
	if code != 0 {
		t.Fatalf("TTY approval exit=%d out=%s err=%s", code, out, errOut)
	}
	var ttyResult struct {
		Gate schema.Gate `json:"gate"`
	}
	if err := json.Unmarshal([]byte(out), &ttyResult); err != nil {
		t.Fatal(err)
	}
	if ttyResult.Gate.ApprovalMethod != "tty" {
		t.Fatalf("TTY gate method = %q", ttyResult.Gate.ApprovalMethod)
	}
	if countGates() != 2 {
		t.Fatalf("gate count = %d, want 2", countGates())
	}
	report := decodeJSON[core.VerifyReport](t, mustRun(t, "--json", "--project", root, "verify"))
	if !report.OK {
		t.Fatalf("verify with token/TTY gates: %+v", report.Issues)
	}
}

func TestRunFinishUsesRemoteCodeAndEnvironment(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "remote-code")
	initDemo(t, root, "remote-code")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1", "--command", "true")
	runID := "T1.default.default.seed0"
	runDir := filepath.Join(root, "progress", "runs", runID)
	remote := map[string]any{
		"run_id": runID, "status": "succeeded", "exit_code": 0,
		"code": map[string]any{
			"repo": "/remote/code", "commit": "remote-commit-sha", "dirty": false,
		},
		"environment": map[string]string{
			"python_version":  "Python 3.12.1",
			"pip_freeze_hash": "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		},
	}
	raw, _ := json.Marshal(remote)
	if err := os.WriteFile(filepath.Join(runDir, "remote_result.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "--json", "--project", root, "run", "finish", runID,
		"--result", filepath.Join(runDir, "remote_result.json"))
	l, err := core.Open(root, "progress")
	if err != nil {
		t.Fatal(err)
	}
	result, err := core.LoadRunResult(l, runID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Code.Commit != "remote-commit-sha" {
		t.Fatalf("result code commit = %q, want remote value", result.Code.Commit)
	}
	if result.Environment["python_version"] != "Python 3.12.1" {
		t.Fatalf("remote environment missing: %+v", result.Environment)
	}
	wantHash, err := core.HashValue(remote["environment"])
	if err != nil {
		t.Fatal(err)
	}
	if result.EnvironmentHash != wantHash {
		t.Fatalf("environment_hash = %s, want %s", result.EnvironmentHash, wantHash)
	}
	statusJSON := mustRun(t, "--json", "--project", root, "run", "status", runID)
	var status struct {
		Result *schema.RunResult `json:"result"`
	}
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		t.Fatal(err)
	}
	if status.Result == nil || status.Result.Code.Commit != "remote-commit-sha" {
		t.Fatalf("run status did not expose remote code: %s", statusJSON)
	}
	human := mustRun(t, "--project", root, "run", "status", runID)
	if !strings.Contains(human, "remote-commit-sha") || !strings.Contains(human, "(remote)") {
		t.Fatalf("run status human output lacks remote commit:\n%s", human)
	}
}

func TestRunnerRecordsRemoteFingerprint(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := t.TempDir()
	workdir := newGitWorkdir(t)
	code, out, errOut := runCLI("--json", "init", "--root", root, "--project", "runner-demo",
		"--host", "local", "--workdir", workdir, "--run-root", "~/.lem-runs")
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errOut)
	}
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	runID := "T1.default.default.seed0"
	runDir := filepath.Join(root, "progress", "runs", runID)
	metricsPath := filepath.Join(runDir, "metrics.json")
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1",
		"--command", "touch "+metricsPath, "--output", "metrics.json")
	cmd := exec.Command(python, filepath.Join(runDir, "runner.py"), "--spec", filepath.Join(runDir, "run_spec.json"))
	cmd.Dir = runDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runner failed: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(filepath.Join(runDir, "remote_result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var remote schema.RemoteResult
	if err := json.Unmarshal(raw, &remote); err != nil {
		t.Fatal(err)
	}
	wantCommit := runGit(t, workdir, "rev-parse", "HEAD")
	if remote.Code.Commit != wantCommit {
		t.Fatalf("runner code commit = %q, want %q", remote.Code.Commit, wantCommit)
	}
	if remote.Code.Dirty {
		t.Fatalf("runner reported a clean workdir as dirty: %+v", remote.Code)
	}
	if remote.Environment["python_version"] == "" {
		t.Fatalf("runner did not record python version: %+v", remote.Environment)
	}
	if !schema.ValidHash(remote.EnvironmentHash) {
		t.Fatalf("runner environment_hash = %q", remote.EnvironmentHash)
	}
	if remote.EnvironmentError != "" {
		t.Fatalf("runner reported an environment error for valid python: %s", remote.EnvironmentError)
	}
	if len(remote.Artifacts) != 1 || !remote.Artifacts[0].Verified {
		t.Fatalf("runner did not mark declared output verified: %+v", remote.Artifacts)
	}
	if remote.Artifacts[0].SHA256 == "" || remote.Artifacts[0].VerifiedAt == "" || remote.Artifacts[0].Host == "" {
		t.Fatalf("runner verification metadata incomplete: %+v", remote.Artifacts[0])
	}
	mustRun(t, "--json", "--project", root, "run", "finish", runID,
		"--result", filepath.Join(runDir, "remote_result.json"))
	artifactReport := decodeJSON[core.ArtifactReport](t, mustRun(t, "--json", "--project", root,
		"artifact", "verify", "--strict"))
	if !artifactReport.OK {
		t.Fatalf("strict artifact verify after runner: %+v", artifactReport.Checks)
	}
	human := mustRun(t, "--project", root, "run", "status", runID)
	if !strings.Contains(human, wantCommit) {
		t.Fatalf("run status does not show remote commit:\n%s", human)
	}
}

func TestArtifactRemoteVerificationAndStrict(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := newProjectRoot(t, "artifact-remote")
	initDemo(t, root, "artifact-remote")
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1", "--command", "true")
	runID := "T1.default.default.seed0"
	runDir := filepath.Join(root, "progress", "runs", runID)
	remote := map[string]any{
		"run_id": runID, "status": "succeeded", "exit_code": 0,
		"artifacts": []map[string]any{
			{
				"name": "checkpoint", "uri": "remote://" + runID + "/checkpoint.pt",
				"sha256":   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"verified": true, "verified_at": "2026-11-03T01:00:00+08:00", "host": "gpu-node",
			},
			{
				"name": "predictions", "uri": "remote://" + runID + "/predictions.npy",
				"sha256": "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			},
		},
	}
	raw, _ := json.Marshal(remote)
	if err := os.WriteFile(filepath.Join(runDir, "remote_result.json"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
	mustRun(t, "--json", "--project", root, "run", "finish", runID,
		"--result", filepath.Join(runDir, "remote_result.json"))

	report := decodeJSON[core.ArtifactReport](t, mustRun(t, "--json", "--project", root, "artifact", "verify"))
	if !report.OK || report.Warnings != 1 || report.Strict {
		t.Fatalf("default artifact report: %+v", report)
	}
	statuses := map[string]string{}
	for _, chk := range report.Checks {
		statuses[chk.Path] = chk.Status
	}
	if statuses["remote://"+runID+"/checkpoint.pt"] != "remote-verified" {
		t.Fatalf("verified remote artifact status = %q", statuses["remote://"+runID+"/checkpoint.pt"])
	}
	if statuses["remote://"+runID+"/predictions.npy"] != "remote-unverified" {
		t.Fatalf("unverified remote artifact status = %q", statuses["remote://"+runID+"/predictions.npy"])
	}
	human := mustRun(t, "--project", root, "artifact", "verify")
	if !strings.Contains(human, "remote-verified") || !strings.Contains(human, "remote-unverified") {
		t.Fatalf("artifact verify human output lacks remote statuses:\n%s", human)
	}

	code, out, _ := runCLI("--json", "--project", root, "artifact", "verify", "--strict")
	if code != 1 {
		t.Fatalf("strict artifact verify exit = %d, want 1\n%s", code, out)
	}
	strictReport := decodeJSON[core.ArtifactReport](t, out)
	if strictReport.OK || !strictReport.Strict || strictReport.Failed != 1 {
		t.Fatalf("strict artifact report: %+v", strictReport)
	}
}

func TestNestedHelp(t *testing.T) {
	cases := [][]string{
		{"run", "start", "--help"},
		{"run", "finish", "--help"},
		{"task", "add", "--help"},
		{"project", "add", "--help"},
		{"artifact", "list", "--help"},
	}
	for _, args := range cases {
		code, out, errOut := runCLI(args...)
		if code != 0 {
			t.Fatalf("%v exit = %d, want 0\nstdout:\n%s\nstderr:\n%s", args, code, out, errOut)
		}
		if !strings.Contains(out, "Usage:") {
			t.Fatalf("%v did not print usage:\n%s", args, out)
		}
	}
}

func TestLocalRemoteWorkdirSeparation(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := t.TempDir()
	local := newGitWorkdir(t)
	remoteOnly := "/remote/ROAD"
	configBytes := []byte("seed: 7\n")
	dataBytes := []byte("local dataset")
	if err := os.MkdirAll(filepath.Join(local, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(local, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "configs", "train.yaml"), configBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(local, "data", "input.bin"), dataBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, local, "add", "configs", "data")
	runGit(t, local, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "inputs")

	code, out, errOut := runCLI("--json", "init", "--root", root, "--project", "local-remote",
		"--host", "local", "--local-workdir", local, "--workdir", remoteOnly, "--run-root", "~/.lem-runs")
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errOut)
	}
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	start := decodeJSON[core.StartRunResult](t, mustRun(t, "--json", "--project", root,
		"run", "start", "--task", "T1", "--command", "true",
		"--config", "configs/train.yaml", "--data", "data/input.bin"))
	spec := start.Spec
	if spec.Workdir != remoteOnly {
		t.Fatalf("workdir = %q, want remote %q", spec.Workdir, remoteOnly)
	}
	wantCommit := runGit(t, local, "rev-parse", "HEAD")
	if spec.Code.Repo != local || spec.Code.Commit != wantCommit {
		t.Fatalf("code did not come from local.workdir: %+v", spec.Code)
	}
	if spec.Code.Dirty {
		t.Fatalf("clean local.workdir reported dirty: %+v", spec.Code)
	}
	if spec.ConfigHash != core.HashFileBytes(configBytes) {
		t.Fatalf("config hash did not come from local.workdir")
	}
	if spec.Data == nil || spec.Data.SHA256 != core.HashFileBytes(dataBytes) {
		t.Fatalf("data hash did not come from local.workdir: %+v", spec.Data)
	}
	if spec.ConfigPath != "configs/train.yaml" || spec.Data.Path != "data/input.bin" {
		t.Fatalf("spec paths should stay relative: config=%q data=%+v", spec.ConfigPath, spec.Data)
	}
	if len(spec.Inputs) != 2 || spec.Inputs[0].Path != "configs/train.yaml" || spec.Inputs[1].Path != "data/input.bin" {
		t.Fatalf("inputs should stay relative for the remote runner: %+v", spec.Inputs)
	}
	remoteTask := mustReadFile(t, start.RemoteTaskPath)
	if strings.Contains(remoteTask, local) || strings.Contains(remoteTask, "local_workdir") {
		t.Fatalf("REMOTE_TASK.md leaked local.workdir:\n%s", remoteTask)
	}
	specBytes, err := os.ReadFile(start.SpecPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(specBytes), "local_workdir") {
		t.Fatalf("run_spec.json still contains local_workdir:\n%s", specBytes)
	}
	l, err := core.Open(root, "progress")
	if err != nil {
		t.Fatal(err)
	}
	events, err := l.ReadEvents()
	if err != nil {
		t.Fatal(err)
	}
	var startEvent *schema.Event
	for i := range events {
		if events[i].Type == "run.start" && events[i].RunID == spec.RunID {
			startEvent = &events[i]
		}
	}
	if startEvent == nil {
		t.Fatal("run.start event not found")
	}
	if startEvent.Payload["local_workdir"] != local || startEvent.Payload["workdir"] != remoteOnly {
		t.Fatalf("local provenance missing from run.start event: %+v", startEvent.Payload)
	}

	absStart := decodeJSON[core.StartRunResult](t, mustRun(t, "--json", "--project", root,
		"run", "start", "--task", "T1", "--run-id", "abs-inputs", "--command", "true",
		"--config", filepath.Join(local, "configs", "train.yaml"),
		"--data", filepath.Join(local, "data", "input.bin")))
	if absStart.Spec.ConfigPath != "configs/train.yaml" || absStart.Spec.Data.Path != "data/input.bin" {
		t.Fatalf("absolute local inputs leaked into spec: config=%q data=%+v", absStart.Spec.ConfigPath, absStart.Spec.Data)
	}
	outside := filepath.Join(filepath.Dir(local), "outside.yaml")
	if err := os.WriteFile(outside, []byte("escape: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runCLI("--project", root, "run", "start", "--task", "T1",
		"--run-id", "escape-config", "--command", "true", "--config", "../outside.yaml")
	if code != 2 || !strings.Contains(errOut, "must not escape") {
		t.Fatalf("escaping config exit=%d err=%s", code, errOut)
	}
	absOutside := filepath.Join(t.TempDir(), "outside.yaml")
	if err := os.WriteFile(absOutside, []byte("escape: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	code, _, errOut = runCLI("--project", root, "run", "start", "--task", "T1",
		"--run-id", "abs-outside-config", "--command", "true", "--config", absOutside)
	if code != 2 || !strings.Contains(errOut, "inside local.workdir") {
		t.Fatalf("absolute outside config exit=%d err=%s", code, errOut)
	}
}

func TestLocalWorkdirMissingFallsBackToAccessibleRemote(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := t.TempDir()
	remoteRepo := newGitWorkdir(t)
	configBytes := []byte("seed: 9\n")
	if err := os.MkdirAll(filepath.Join(remoteRepo, "configs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remoteRepo, "configs", "train.yaml"), configBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, remoteRepo, "add", "configs")
	runGit(t, remoteRepo, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "cfg")
	missingLocal := filepath.Join(t.TempDir(), "missing-clone")
	code, out, errOut := runCLI("--json", "init", "--root", root, "--project", "fallback",
		"--host", "local", "--local-workdir", missingLocal, "--workdir", remoteRepo, "--run-root", "~/.lem-runs")
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errOut)
	}
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	start := decodeJSON[core.StartRunResult](t, mustRun(t, "--json", "--project", root,
		"run", "start", "--task", "T1", "--command", "true", "--config", "configs/train.yaml"))
	wantCommit := runGit(t, remoteRepo, "rev-parse", "HEAD")
	if start.Spec.Code.Repo != remoteRepo || start.Spec.Code.Commit != wantCommit {
		t.Fatalf("missing local.workdir did not fall back to accessible remote.workdir: %+v", start.Spec.Code)
	}
	if start.Spec.ConfigHash != core.HashFileBytes(configBytes) {
		t.Fatal("config was not resolved from accessible remote.workdir fallback")
	}
}

func TestDoctorLocalWorkdir(t *testing.T) {
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	remoteOnly := "/remote/ROAD"
	readChecks := func(root string) map[string]doctorCheck {
		out := mustRun(t, "--json", "--project", root, "doctor")
		var report struct {
			OK     bool          `json:"ok"`
			Checks []doctorCheck `json:"checks"`
		}
		if err := json.Unmarshal([]byte(out), &report); err != nil {
			t.Fatal(err)
		}
		if !report.OK {
			t.Fatalf("doctor reported failure for warnings: %+v", report.Checks)
		}
		m := map[string]doctorCheck{}
		for _, chk := range report.Checks {
			m[chk.Name] = chk
		}
		return m
	}
	initWithLocal := func(t *testing.T, local, remote string) string {
		t.Helper()
		root := t.TempDir()
		code, out, errOut := runCLI("--json", "init", "--root", root, "--project", "doctor-local",
			"--host", "local", "--local-workdir", local, "--workdir", remote, "--run-root", "~/.lem-runs")
		if code != 0 {
			t.Fatalf("init: %d %s %s", code, out, errOut)
		}
		return root
	}

	missingRoot := initWithLocal(t, filepath.Join(t.TempDir(), "missing-clone"), remoteOnly)
	checks := readChecks(missingRoot)
	if checks["local workdir"].Status != "warn" || !strings.Contains(checks["local workdir"].Detail, "missing") {
		t.Fatalf("missing local.workdir check: %+v", checks["local workdir"])
	}
	if checks["remote workdir"].Status != "ok" || !strings.Contains(checks["remote workdir"].Detail, "remote-only, not checked locally") {
		t.Fatalf("remote workdir check: %+v", checks["remote workdir"])
	}
	if checks["local code fallback"].Status != "warn" {
		t.Fatalf("unexpected fallback check without accessible remote: %+v", checks["local code fallback"])
	}

	fallbackRemote := newGitWorkdir(t)
	fallbackRoot := initWithLocal(t, filepath.Join(t.TempDir(), "missing-clone"), fallbackRemote)
	checks = readChecks(fallbackRoot)
	if checks["local workdir"].Status != "warn" {
		t.Fatalf("missing local.workdir check: %+v", checks["local workdir"])
	}
	if checks["local code fallback"].Status != "ok" || !strings.Contains(checks["local code fallback"].Detail, "falling back") {
		t.Fatalf("fallback wording: %+v", checks["local code fallback"])
	}
	if !strings.Contains(checks["remote workdir"].Detail, "locally accessible for fallback only") {
		t.Fatalf("remote workdir fallback hint: %+v", checks["remote workdir"])
	}

	nonGit := t.TempDir()
	nonGitRoot := initWithLocal(t, nonGit, remoteOnly)
	checks = readChecks(nonGitRoot)
	if checks["local workdir"].Status != "warn" || !strings.Contains(checks["local workdir"].Detail, "not a git repository") {
		t.Fatalf("non-git local.workdir check: %+v", checks["local workdir"])
	}

	dirtyRepo := newGitWorkdir(t)
	if err := os.WriteFile(filepath.Join(dirtyRepo, "README.md"), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dirtyRoot := initWithLocal(t, dirtyRepo, remoteOnly)
	checks = readChecks(dirtyRoot)
	if checks["local workdir status"].Status != "warn" || !strings.Contains(checks["local workdir status"].Detail, "dirty") {
		t.Fatalf("dirty local.workdir check: %+v", checks["local workdir status"])
	}

	cleanRepo := newGitWorkdir(t)
	cleanRoot := initWithLocal(t, cleanRepo, remoteOnly)
	checks = readChecks(cleanRoot)
	if checks["local workdir"].Status != "ok" || checks["local workdir status"].Status != "ok" {
		t.Fatalf("clean local.workdir checks: %+v %+v", checks["local workdir"], checks["local workdir status"])
	}
}

func TestRunnerEnvironmentFailureIsExplicit(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := t.TempDir()
	workdir := newGitWorkdir(t)
	code, out, errOut := runCLI("--json", "init", "--root", root, "--project", "env-fail",
		"--host", "local", "--workdir", workdir, "--python", "/nonexistent/python", "--run-root", "~/.lem-runs")
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errOut)
	}
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1",
		"--command", "true", "--env", "PATH=/nonexistent")
	runID := "T1.default.default.seed0"
	runDir := filepath.Join(root, "progress", "runs", runID)
	cmd := exec.Command(python, filepath.Join(runDir, "runner.py"), "--spec", filepath.Join(runDir, "run_spec.json"))
	cmd.Dir = runDir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("runner failed: %v\n%s", err, output)
	}
	raw, err := os.ReadFile(filepath.Join(runDir, "remote_result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var remote schema.RemoteResult
	if err := json.Unmarshal(raw, &remote); err != nil {
		t.Fatal(err)
	}
	if remote.EnvironmentHash != "" {
		t.Fatalf("failed environment must not have a hash: %q", remote.EnvironmentHash)
	}
	if remote.EnvironmentError == "" || !strings.Contains(remote.EnvironmentError, "/nonexistent/python") {
		t.Fatalf("environment_error = %q", remote.EnvironmentError)
	}
	if len(remote.Environment) != 0 {
		t.Fatalf("failed environment should be empty: %+v", remote.Environment)
	}
	manifest := decodeJSON[schema.Manifest](t, mustReadFile(t, filepath.Join(runDir, "manifest.json")))
	if manifest.EnvironmentError == "" {
		t.Fatal("manifest missing environment_error")
	}
	mustRun(t, "--json", "--project", root, "run", "finish", runID,
		"--result", filepath.Join(runDir, "remote_result.json"))
	l, err := core.Open(root, "progress")
	if err != nil {
		t.Fatal(err)
	}
	result, err := core.LoadRunResult(l, runID)
	if err != nil {
		t.Fatal(err)
	}
	if result.EnvironmentError == "" || result.EnvironmentHash != "" {
		t.Fatalf("result environment error/hash = %q / %q", result.EnvironmentError, result.EnvironmentHash)
	}
	human := mustRun(t, "--project", root, "run", "status", runID)
	if !strings.Contains(human, "env:     unavailable") || !strings.Contains(human, "/nonexistent/python") {
		t.Fatalf("run status did not show unavailable environment:\n%s", human)
	}
	if strings.Contains(human, "env:     sha256:") {
		t.Fatalf("run status displayed an empty-environment hash:\n%s", human)
	}
}

func TestRunnerInputResolution(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	t.Setenv("LEM_CONFIG_DIR", t.TempDir())
	t.Setenv("LEM_FAILPOINT", "")
	root := t.TempDir()
	workdir := newGitWorkdir(t)
	configPath := filepath.Join(workdir, "configs", "train.yaml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("seed: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, workdir, "add", "configs")
	runGit(t, workdir, "-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "-m", "config")

	code, out, errOut := runCLI("--json", "init", "--root", root, "--project", "inputs",
		"--host", "local", "--local-workdir", workdir, "--workdir", workdir, "--run-root", "~/.lem-runs")
	if code != 0 {
		t.Fatalf("init: %d %s %s", code, out, errOut)
	}
	mustRun(t, "--json", "--project", root, "task", "add", "--id", "T1", "--title", "task")
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1",
		"--command", "true", "--config", "configs/train.yaml")
	runID := "T1.default.default.seed0"
	runDir := filepath.Join(root, "progress", "runs", runID)
	uriRunID := "uri-input"
	uriRunDir := filepath.Join(root, "progress", "runs", uriRunID)
	uriHash := "sha256:" + strings.Repeat("a", 64)
	mustRun(t, "--json", "--project", root, "run", "start", "--task", "T1",
		"--run-id", uriRunID, "--command", "true", "--data", "ssh://gpu/data/input.bin", "--data-sha256", uriHash)

	runRunner := func(dir, cwd string) schema.RemoteResult {
		t.Helper()
		cmd := exec.Command(python, filepath.Join(dir, "runner.py"), "--spec", filepath.Join(dir, "run_spec.json"))
		cmd.Dir = cwd
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("runner from %s failed to execute: %v\n%s", cwd, err, output)
		}
		raw, err := os.ReadFile(filepath.Join(dir, "remote_result.json"))
		if err != nil {
			t.Fatal(err)
		}
		var res schema.RemoteResult
		if err := json.Unmarshal(raw, &res); err != nil {
			t.Fatal(err)
		}
		return res
	}

	fromRunDir := runRunner(runDir, runDir)
	fromProjectRoot := runRunner(runDir, root)
	if fromRunDir.Status != "succeeded" || fromRunDir.ExitCode != 0 {
		t.Fatalf("relative input from run dir: %+v", fromRunDir)
	}
	if fromProjectRoot.Status != fromRunDir.Status || fromProjectRoot.ExitCode != fromRunDir.ExitCode || fromProjectRoot.Error != fromRunDir.Error {
		t.Fatalf("runner cwd changed result: run=%+v root=%+v", fromRunDir, fromProjectRoot)
	}

	if err := os.WriteFile(configPath, []byte("seed: 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mismatch := runRunner(runDir, runDir)
	if mismatch.Status != "failed" || mismatch.ExitCode != 3 || !strings.Contains(mismatch.Error, "input hash mismatch") {
		t.Fatalf("changed input: %+v", mismatch)
	}

	if err := os.Remove(configPath); err != nil {
		t.Fatal(err)
	}
	missing := runRunner(runDir, runDir)
	if missing.Status != "failed" || missing.ExitCode != 3 || missing.Error != "input missing: configs/train.yaml" {
		t.Fatalf("missing input: %+v", missing)
	}

	uri := runRunner(uriRunDir, uriRunDir)
	if uri.Status != "succeeded" || uri.ExitCode != 0 || uri.Error != "" {
		t.Fatalf("URI input was not skipped: %+v", uri)
	}
}
