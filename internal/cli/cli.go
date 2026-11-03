package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/buerka/lemma/internal/core"
)

// Version is overridden by GoReleaser via -ldflags at release time.
var Version = "0.1.0"

type Globals struct {
	Project     string
	JSON        bool
	Verbose     bool
	Version     bool
	LockTimeout time.Duration
}

type Context struct {
	In       io.Reader
	Out      io.Writer
	Err      io.Writer
	Globals  Globals
	ExitCode int
	IsTTY    bool
}

// Run is the production entrypoint. TTY detection is deliberately local: the
// approval gate is a workflow constraint, not a cryptographic proof.
func Run(args []string, out, errOut io.Writer) int {
	isTTY := false
	if info, err := os.Stdin.Stat(); err == nil {
		isTTY = info.Mode()&os.ModeCharDevice != 0
	}
	return RunWithIO(args, os.Stdin, out, errOut, isTTY)
}

// RunWithIO allows tests to inject stdin and the TTY decision.
func RunWithIO(args []string, in io.Reader, out, errOut io.Writer, isTTY bool) int {
	if in == nil {
		in = os.Stdin
	}
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		printUsage(out)
		return 0
	}
	g, rest, err := parseGlobals(args)
	ctx := &Context{In: in, Out: out, Err: errOut, Globals: g, IsTTY: isTTY}
	if err != nil {
		return reportError(ctx, err)
	}
	if g.Version {
		fmt.Fprintf(out, "lem %s\n", Version)
		return 0
	}
	if len(rest) == 0 {
		printUsage(out)
		return 0
	}
	cmd := rest[0]
	rest = rest[1:]
	if cmd == "help" {
		printUsage(out)
		return 0
	}
	fn, ok := commands[cmd]
	if !ok {
		return reportError(ctx, core.Errorf(core.CodeUsage, "unknown command %q (run lem help)", cmd))
	}
	if err := fn(ctx, rest); err != nil {
		return reportError(ctx, err)
	}
	return ctx.ExitCode
}

var commands = map[string]func(*Context, []string) error{
	"init":     cmdInit,
	"doctor":   cmdDoctor,
	"task":     cmdTask,
	"run":      cmdRun,
	"status":   cmdStatus,
	"next":     cmdNext,
	"plan":     cmdPlan,
	"review":   cmdReview,
	"approve":  cmdApprove,
	"apply":    cmdApply,
	"log":      cmdLog,
	"show":     cmdShow,
	"verify":   cmdVerify,
	"project":  cmdProject,
	"artifact": cmdArtifact,
	"migrate":  cmdMigrate,
	"version": func(c *Context, _ []string) error {
		fmt.Fprintf(c.Out, "lem %s\n", Version)
		return nil
	},
}

func parseGlobals(args []string) (Globals, []string, error) {
	g := Globals{LockTimeout: 5 * time.Second}
	if v := os.Getenv("LEM_LOCK_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			g.LockTimeout = d
		}
	}
	var rest []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--json":
			g.JSON = true
		case a == "--verbose" || a == "-v":
			g.Verbose = true
		case a == "--version":
			g.Version = true
		case a == "--project":
			if i+1 >= len(args) {
				return g, nil, core.Errorf(core.CodeUsage, "--project requires a value")
			}
			i++
			g.Project = args[i]
		case strings.HasPrefix(a, "--project="):
			g.Project = strings.TrimPrefix(a, "--project=")
		case a == "--lock-timeout":
			if i+1 >= len(args) {
				return g, nil, core.Errorf(core.CodeUsage, "--lock-timeout requires a duration")
			}
			i++
			d, err := time.ParseDuration(args[i])
			if err != nil || d <= 0 {
				return g, nil, core.Errorf(core.CodeUsage, "invalid --lock-timeout %q", args[i])
			}
			g.LockTimeout = d
		case strings.HasPrefix(a, "--lock-timeout="):
			v := strings.TrimPrefix(a, "--lock-timeout=")
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				return g, nil, core.Errorf(core.CodeUsage, "invalid --lock-timeout %q", v)
			}
			g.LockTimeout = d
		default:
			rest = append(rest, a)
		}
	}
	return g, rest, nil
}

func printUsage(w io.Writer) {
	fmt.Fprint(w, `lem — a git-native progress ledger for human–agent research

Usage:
  lem [--project <id|path>] [--json] [--lock-timeout 5s] <command> [flags]

Commands:
  init                     Initialize progress/ as an independent git ledger
  doctor                   Check local configuration and SSH host aliases
  project add|list|remove  Manage the multi-project registry
  task add|update|list     Manage the task graph
  run start|finish|status  Start/finish a run, or inspect a run
  status                   Print a state snapshot and refresh STATE.md
  next                     Print the next executable task
  plan                     Create a changeset from current state
  review                   Render REVIEW.md for a changeset
  approve                  Record a human approval in gates.jsonl
  apply                    Atomically apply an approved changeset
  log                      Query the event log
  show                     Show a changeset or receipt
  verify                   Verify schema, hash chain, gates, receipts, git
  artifact list|verify     Inspect local run manifests
  migrate                  Check the ledger schema version
  version                  Print the version

Project resolution: --project, then LEM_PROJECT, then progress/project.yaml
from cwd upward, then the registry default (~/.config/lem/projects.yaml).

Exit codes: 0 ok, 1 general, 2 usage, 3 conflict, 4 busy, 5 idempotent, 6 stale.
`)
}

func (c *Context) jsonOut(v any) error {
	enc := json.NewEncoder(c.Out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return core.Wrap(core.CodeGeneral, "write json output", err)
	}
	return nil
}

func parseFlags(c *Context, fs *flag.FlagSet, args []string, usage string) (bool, error) {
	fs.SetOutput(io.Discard)
	fs.Usage = func() { fmt.Fprint(c.Out, usage) }
	if err := fs.Parse(interleaveFlags(fs, args)); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fs.Usage()
			return false, nil
		}
		return false, core.Errorf(core.CodeUsage, "%v", err)
	}
	return true, nil
}

// interleaveFlags moves flags ahead of positional arguments so commands such as
// "lem run finish RUN --result FILE" and "lem task update T1 --status done"
// work (the standard flag package stops at the first positional).
func interleaveFlags(fs *flag.FlagSet, args []string) []string {
	var flags, positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if len(a) < 2 || a[0] != '-' || a == "-" {
			positional = append(positional, a)
			continue
		}
		name := strings.TrimLeft(a, "-")
		if eq := strings.IndexByte(name, '='); eq >= 0 {
			name = name[:eq]
		}
		flags = append(flags, a)
		f := fs.Lookup(name)
		if f == nil || strings.Contains(a, "=") {
			continue
		}
		if bf, ok := f.Value.(interface{ IsBoolFlag() bool }); ok && bf.IsBoolFlag() {
			continue
		}
		if i+1 < len(args) {
			i++
			flags = append(flags, args[i])
		}
	}
	return append(flags, positional...)
}

type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func (c *Context) resolveProject() (*core.Resolved, error) {
	ref := c.Globals.Project
	if ref == "" {
		ref = os.Getenv("LEM_PROJECT")
	}
	cwd, err := os.Getwd()
	if err != nil {
		return nil, core.Wrap(core.CodeGeneral, "get working directory", err)
	}
	return core.ResolveProject(ref, cwd)
}

func (c *Context) openLedger() (*core.Ledger, *core.Resolved, error) {
	res, err := c.resolveProject()
	if err != nil {
		return nil, nil, err
	}
	l, err := core.Open(res.Root, res.Ledger)
	if err != nil {
		return nil, nil, err
	}
	return l, res, nil
}

// resolveChangesetRef accepts ids as well as paths relative to cwd, the project
// root, or the ledger directory.
func resolveChangesetRef(l *core.Ledger, ref string) string {
	if ref == "" || filepath.IsAbs(ref) {
		return ref
	}
	if _, err := os.Stat(ref); err == nil {
		return ref
	}
	for _, base := range []string{l.Root, l.Dir} {
		candidate := base + string(os.PathSeparator) + ref
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return ref
}

func reportError(c *Context, err error) int {
	code := core.CodeOf(err)
	exit := 1
	switch code {
	case core.CodeUsage:
		exit = 2
	case core.CodeConflict:
		exit = 3
	case core.CodeBusy:
		exit = 4
	case core.CodeIdempotent:
		exit = 5
	case core.CodeStale:
		exit = 6
	}
	if c.Globals.JSON {
		payload := map[string]any{
			"ok": false,
			"error": map[string]any{
				"code":    code,
				"message": err.Error(),
			},
		}
		_ = c.jsonOut(payload)
	} else {
		fmt.Fprintf(c.Err, "lem: %s\n", err.Error())
	}
	return exit
}
