package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"

	"github.com/buerka/lemma/internal/core"
)

func cmdProject(c *Context, args []string) error {
	if len(args) == 0 {
		return core.Errorf(core.CodeUsage, "project requires a subcommand: add, list, remove")
	}
	switch args[0] {
	case "add":
		return cmdProjectAdd(c, args[1:])
	case "list":
		return cmdProjectList(c, args[1:])
	case "remove":
		return cmdProjectRemove(c, args[1:])
	default:
		return core.Errorf(core.CodeUsage, "unknown project subcommand %q", args[0])
	}
}

func cmdProjectAdd(c *Context, args []string) error {
	fs := flag.NewFlagSet("project add", flag.ContinueOnError)
	root := fs.String("root", ".", "project root")
	ledger := fs.String("ledger", "progress", "ledger directory")
	setDefault := fs.Bool("default", false, "set as the registry default")
	force := fs.Bool("force", false, "replace an existing entry pointing elsewhere")
	usage := "Usage: lem project add <id> --root <path> [--ledger progress] [--default] [--force]\n"
	ok, err := parseFlags(c, fs, args, usage)
	if !ok {
		return err
	}
	if fs.NArg() != 1 {
		return core.Errorf(core.CodeUsage, "project add requires exactly one project id")
	}
	id := fs.Arg(0)
	cwd, err := os.Getwd()
	if err != nil {
		return core.Wrap(core.CodeGeneral, "get working directory", err)
	}
	rootPath := *root
	if !filepath.IsAbs(rootPath) {
		rootPath = cwd + string(os.PathSeparator) + rootPath
	}
	r, err := core.AddProject(id, rootPath, *ledger, *setDefault, *force, c.Globals.LockTimeout)
	if err != nil {
		return err
	}
	p := r.Projects[id]
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{"id": id, "root": p.Root, "ledger": p.Ledger, "default": r.Default, "registry": core.RegistryPath()})
	}
	fmt.Fprintf(c.Out, "registered %s -> %s (ledger %s)\n", id, p.Root, p.Ledger)
	if r.Default == id {
		fmt.Fprintln(c.Out, "registry default: "+id)
	}
	return nil
}

func cmdProjectList(c *Context, args []string) error {
	fs := flag.NewFlagSet("project list", flag.ContinueOnError)
	ok, err := parseFlags(c, fs, args, "Usage: lem project list\n")
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "project list takes no arguments")
	}
	r, err := core.LoadRegistry()
	if err != nil {
		return err
	}
	type row struct {
		ID      string `json:"id"`
		Root    string `json:"root"`
		Ledger  string `json:"ledger"`
		Default bool   `json:"default"`
	}
	rows := []row{}
	for _, id := range r.SortedIDs() {
		p := r.Projects[id]
		ledger := p.Ledger
		if ledger == "" {
			ledger = "progress"
		}
		rows = append(rows, row{ID: id, Root: p.Root, Ledger: ledger, Default: r.Default == id})
	}
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{"default": r.Default, "projects": rows, "registry": core.RegistryPath()})
	}
	if len(rows) == 0 {
		fmt.Fprintln(c.Out, "no registered projects")
		fmt.Fprintln(c.Out, "registry: "+core.RegistryPath())
		return nil
	}
	tw := tabwriter.NewWriter(c.Out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tDEFAULT\tLEDGER\tROOT")
	for _, row := range rows {
		def := ""
		if row.Default {
			def = "*"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", row.ID, def, row.Ledger, row.Root)
	}
	_ = tw.Flush()
	fmt.Fprintln(c.Out, "registry: "+core.RegistryPath())
	return nil
}

func cmdProjectRemove(c *Context, args []string) error {
	fs := flag.NewFlagSet("project remove", flag.ContinueOnError)
	ok, err := parseFlags(c, fs, args, "Usage: lem project remove <id>\n")
	if !ok {
		return err
	}
	if fs.NArg() != 1 {
		return core.Errorf(core.CodeUsage, "project remove requires exactly one project id")
	}
	id := fs.Arg(0)
	r, err := core.RemoveProject(id, c.Globals.LockTimeout)
	if err != nil {
		return err
	}
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{"removed": id, "default": r.Default})
	}
	fmt.Fprintf(c.Out, "removed %s (default: %s)\n", id, r.Default)
	return nil
}
