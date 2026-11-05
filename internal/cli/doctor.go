package cli

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"

	"github.com/buerka/lemma/internal/core"
	"github.com/buerka/lemma/internal/schema"
)

type doctorCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"` // ok | warn | fail
	Detail string `json:"detail"`
}

func cmdDoctor(c *Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	ok, err := parseFlags(c, fs, args, "Usage: lem doctor [--json]\n")
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "doctor takes no arguments")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	var checks []doctorCheck
	add := func(name, status, detail string) {
		checks = append(checks, doctorCheck{Name: name, Status: status, Detail: detail})
	}

	if out, err := exec.Command("git", "--version").CombinedOutput(); err != nil {
		add("git", "fail", strings.TrimSpace(string(out)))
	} else {
		add("git", "ok", strings.TrimSpace(string(out)))
	}
	if core.IsGitRepo(l.Dir) {
		add("progress git", "ok", l.Dir+" is an independent git repository")
	} else {
		add("progress git", "fail", "progress/.git is missing; run lem init or git init")
	}
	if clean, err := (core.Git{Dir: l.Dir}).IsClean(); err != nil {
		add("progress status", "warn", err.Error())
	} else if clean {
		add("progress status", "ok", "working tree clean")
	} else {
		add("progress status", "warn", "progress/ has uncommitted changes (run lem verify)")
	}
	add("schema", "ok", fmt.Sprintf("schema_version=%d", l.Project.SchemaVersion))
	if _, err := l.LoadTasks(); err != nil {
		add("tasks.yaml", "fail", err.Error())
	} else {
		add("tasks.yaml", "ok", "parsed and validated")
	}
	missing := []string{}
	if l.Project.Remote.Host == "" {
		missing = append(missing, "remote.host")
	}
	if l.Project.Remote.Workdir == "" {
		missing = append(missing, "remote.workdir")
	}
	if l.Project.Remote.RunRoot == "" {
		missing = append(missing, "remote.run_root")
	}
	if len(missing) > 0 {
		add("project.yaml", "warn", "missing "+strings.Join(missing, ", "))
	} else {
		add("project.yaml", "ok", "remote fields present")
	}

	local := l.LocalWorkdir()
	rawRemote := strings.TrimSpace(l.Project.Remote.Workdir)
	remotePath := l.RemoteWorkdirPath()
	remoteAccessible := false
	if remotePath != "" && schema.ValidRemoteWorkdir(rawRemote) {
		if st, err := os.Stat(remotePath); err == nil && st.IsDir() {
			remoteAccessible = true
		}
	}
	switch {
	case rawRemote == "":
		add("remote workdir", "warn", "remote.workdir is not configured")
	case !schema.ValidRemoteWorkdir(rawRemote):
		add("remote workdir", "warn", "remote.workdir must be absolute or start with ~/: "+rawRemote)
	case local == "" && remoteAccessible:
		add("remote workdir", "ok", remotePath+" (locally accessible; used as fallback)")
	default:
		add("remote workdir", "ok", "remote-only (not checked locally): "+remotePath)
	}

	localUsable := false
	localIssue := ""
	if local != "" {
		st, err := os.Stat(local)
		switch {
		case err != nil:
			localIssue = "local.workdir is missing: " + local
		case !st.IsDir():
			localIssue = "local.workdir is not a directory: " + local
		default:
			repo := core.GitTopLevel(local)
			if repo == "" {
				localIssue = "local.workdir is not a git repository: " + local
			} else {
				localUsable = true
				add("local workdir", "ok", local+" (git repo "+repo+")")
				if clean, err := (core.Git{Dir: repo}).IsClean(); err != nil {
					add("local workdir status", "warn", err.Error())
				} else if clean {
					add("local workdir status", "ok", "git working tree clean")
				} else {
					add("local workdir status", "warn", "git working tree is dirty; run provenance will include a patch hash")
				}
			}
		}
		if localIssue != "" {
			add("local workdir", "warn", localIssue+"; code provenance will be incomplete")
		}
	}
	if !localUsable {
		switch {
		case remoteAccessible:
			reason := "local.workdir is not configured"
			if localIssue != "" {
				reason = localIssue
			}
			add("local code fallback", "ok", reason+"; falling back to locally accessible remote.workdir: "+remotePath)
		case localIssue != "":
			add("local code fallback", "warn", localIssue+" and remote.workdir is not locally accessible; code provenance will be incomplete")
		default:
			add("local code fallback", "warn", "local.workdir is not set and remote.workdir is not locally accessible; set local.workdir for local code provenance")
		}
	}
	python := l.Project.Remote.Python
	if python == "" {
		python = "python3"
	}
	if p, err := exec.LookPath(python); err != nil {
		add("local python", "warn", fmt.Sprintf("%s not found locally (remote may still have it)", python))
	} else {
		add("local python", "ok", p)
	}

	host := l.Project.Remote.Host
	if host == "" || host == "local" {
		add("ssh host", "ok", "local project; no SSH host alias required")
	} else {
		found, detail := checkSSHHost(host)
		if found {
			add("ssh host", "ok", detail)
		} else {
			add("ssh host", "warn", detail)
		}
	}
	if r, err := core.LoadRegistry(); err == nil {
		detail := fmt.Sprintf("%d registered project(s), default=%q", len(r.Projects), r.Default)
		add("registry", "ok", detail+" ("+core.RegistryPath()+")")
	} else {
		add("registry", "warn", err.Error())
	}
	add("ssh-mcp tools", "ok", "ssh_hosts, ssh_exec, ssh_output, ssh_put, ssh_get, ssh_ls, ssh_forward")

	failed := 0
	for _, chk := range checks {
		if chk.Status == "fail" {
			failed++
		}
	}
	if c.Globals.JSON {
		if failed > 0 {
			c.ExitCode = 1
		}
		return c.jsonOut(map[string]any{"ok": failed == 0, "project": l.Project.Project, "checks": checks})
	}
	for _, chk := range checks {
		fmt.Fprintf(c.Out, "%-4s %-16s %s\n", strings.ToUpper(chk.Status), chk.Name, chk.Detail)
	}
	if failed > 0 {
		c.ExitCode = 1
		return nil
	}
	fmt.Fprintln(c.Out, "\nchecks passed; lem does not connect to the remote host itself")
	return nil
}

func checkSSHHost(host string) (bool, string) {
	home, err := os.UserHomeDir()
	if err != nil {
		return false, "cannot determine home directory"
	}
	configPath := filepath.Join(home, ".ssh", "config")
	raw, err := os.ReadFile(configPath)
	if err != nil {
		return false, fmt.Sprintf("%s not readable; add a Host %s alias", configPath, host)
	}
	lines := strings.Split(string(raw), "\n")
	var patterns []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "Host") {
			continue
		}
		patterns = append(patterns, fields[1:]...)
	}
	for _, p := range patterns {
		if strings.EqualFold(p, host) {
			return true, fmt.Sprintf("Host %s found in %s", host, configPath)
		}
	}
	for _, p := range patterns {
		if ok, _ := path.Match(p, host); ok {
			return true, fmt.Sprintf("Host pattern %q matches %s in %s", p, host, configPath)
		}
	}
	return false, fmt.Sprintf("Host %s not found in %s", host, configPath)
}
