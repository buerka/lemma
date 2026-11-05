package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/buerka/lemma/internal/core"
	"github.com/buerka/lemma/internal/schema"
)

func cmdInit(c *Context, args []string) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	projectName := fs.String("project", c.Globals.Project, "project name")
	host := fs.String("host", "local", "ssh-mcp host alias")
	workdir := fs.String("workdir", "~/project", "remote working directory")
	localWorkdir := fs.String("local-workdir", "", "local clone used for code/config/data provenance")
	runRoot := fs.String("run-root", "~/.lem-runs", "remote run directory")
	python := fs.String("python", "python3", "remote Python interpreter")
	ledger := fs.String("ledger", "progress", "ledger directory name")
	root := fs.String("root", "", "project root (default: current directory)")
	artifactRoot := fs.String("artifact-root", "", "artifact URI root (default derived)")
	usage := `Usage: lem init [--project NAME] [--host gpu] [--local-workdir DIR]
                [--workdir REMOTE_DIR] [--run-root DIR]

Creates <root>/<ledger>/ as an independent git repository with project.yaml,
tasks.yaml, events.jsonl, gates.jsonl, changesets/, receipts/, runs/, STATE.md
and DECISIONS.md.
`
	ok, err := parseFlags(c, fs, args, usage)
	if ok != true {
		return err
	}
	_ = ok
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "init does not accept positional arguments")
	}
	if *root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return core.Wrap(core.CodeGeneral, "get working directory", err)
		}
		*root = cwd
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		return core.Wrap(core.CodeUsage, "resolve --root", err)
	}
	name := strings.TrimSpace(*projectName)
	if name == "" {
		name = filepath.Base(absRoot)
	}
	storage := *artifactRoot
	if storage == "" {
		if *host != "" && *host != "local" {
			storage = "ssh://" + *host + "/" + strings.TrimLeft(*runRoot, "/")
		} else {
			storage = *runRoot
		}
	}
	p := schema.Project{
		Project:       name,
		SchemaVersion: schema.SchemaVersion,
		Local:         schema.LocalConfig{Workdir: *localWorkdir},
		Remote: schema.RemoteConfig{
			Transport: "ssh-mcp",
			Host:      *host,
			Workdir:   *workdir,
			RunRoot:   *runRoot,
			Python:    *python,
		},
		Storage: schema.StorageConfig{ArtifactRoot: storage},
	}
	l, ev, err := core.InitLedger(core.InitOptions{
		Root:     absRoot,
		Ledger:   *ledger,
		Project:  p,
		LockWait: c.Globals.LockTimeout,
	})
	if err != nil {
		return err
	}
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{
			"project":        p.Project,
			"root":           l.Root,
			"ledger":         l.Dir,
			"local_workdir":  p.Local.Workdir,
			"remote_workdir": p.Remote.Workdir,
			"revision":       ev.Hash,
			"event":          ev,
		})
	}
	fmt.Fprintf(c.Out, "initialized %s\n", p.Project)
	fmt.Fprintf(c.Out, "  root:     %s\n", l.Root)
	fmt.Fprintf(c.Out, "  ledger:   %s\n", l.Dir)
	if p.Local.Workdir != "" {
		fmt.Fprintf(c.Out, "  local:    %s\n", l.LocalWorkdir())
	}
	fmt.Fprintf(c.Out, "  remote:   %s\n", p.Remote.Workdir)
	fmt.Fprintf(c.Out, "  revision: %s\n", ev.Hash)
	fmt.Fprintln(c.Out, "\nNext: lem task add --id T1 --title \"first task\" --phase P0")
	return nil
}
