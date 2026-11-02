package cli

import (
	"flag"
	"fmt"
	"text/tabwriter"

	"github.com/buerka/lemma/internal/core"
	"github.com/buerka/lemma/internal/schema"
)

func cmdTask(c *Context, args []string) error {
	if len(args) == 0 {
		return core.Errorf(core.CodeUsage, "task requires a subcommand: add, update, list")
	}
	switch args[0] {
	case "add":
		return cmdTaskAdd(c, args[1:])
	case "update":
		return cmdTaskUpdate(c, args[1:])
	case "list":
		return cmdTaskList(c, args[1:])
	default:
		return core.Errorf(core.CodeUsage, "unknown task subcommand %q", args[0])
	}
}

func cmdTaskAdd(c *Context, args []string) error {
	fs := flag.NewFlagSet("task add", flag.ContinueOnError)
	id := fs.String("id", "", "task id")
	title := fs.String("title", "", "task title")
	phase := fs.String("phase", "", "phase, for example P0")
	owner := fs.String("owner", schema.OwnerAgent, "owner: agent|human")
	status := fs.String("status", schema.StatusTodo, "status: todo|doing|blocked|done")
	expected := fs.String("expected-revision", "", "require this current revision")
	var acceptance, depends, artifacts stringList
	fs.Var(&acceptance, "acceptance", "acceptance criterion (repeatable)")
	fs.Var(&depends, "depends-on", "task dependency id (repeatable)")
	fs.Var(&artifacts, "artifact", "artifact reference (repeatable)")
	usage := `Usage: lem task add --id T1 --title "baseline reproduction" [--phase P0]
  [--owner agent|human] [--status todo|doing|blocked|done]
  [--acceptance "..."] [--depends-on T0] [--artifact progress/artifacts/...]

`
	ok, err := parseFlags(c, fs, args, usage)
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "task add does not accept positional arguments")
	}
	if *id == "" || *title == "" {
		return core.Errorf(core.CodeUsage, "task add requires --id and --title")
	}
	if !schema.ValidTaskStatus(*status) {
		return core.Errorf(core.CodeUsage, "invalid --status %q", *status)
	}
	if !schema.ValidOwner(*owner) {
		return core.Errorf(core.CodeUsage, "invalid --owner %q", *owner)
	}

	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	lock, err := core.AcquireLock(l.Dir, c.Globals.LockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	if err := core.EnsureClean(l); err != nil {
		return err
	}
	if err := core.CheckExpectedRevision(l, *expected); err != nil {
		return err
	}

	tasks, err := l.LoadTasks()
	if err != nil {
		return err
	}
	if tasks.Find(*id) != nil {
		return core.Errorf(core.CodeConflict, "task %s already exists", *id)
	}
	task := schema.Task{
		ID: *id, Phase: *phase, Title: *title, Owner: *owner, Status: *status,
		DependsOn: depends, Acceptance: acceptance, Artifacts: artifacts,
		UpdatedAt: l.NowUTC(),
	}
	tasks.Tasks = append(tasks.Tasks, task)
	if err := l.SaveTasks(tasks); err != nil {
		return err
	}
	if _, err := l.AppendEvent("task.add", task.ID, "", "", map[string]any{
		"id": task.ID, "title": task.Title, "phase": task.Phase,
		"owner": task.Owner, "status": task.Status,
		"depends_on": task.DependsOn, "acceptance": task.Acceptance,
	}); err != nil {
		return err
	}
	if _, err := l.WriteState(true); err != nil {
		return err
	}
	if _, _, err := l.Commit("task: add " + task.ID); err != nil {
		return core.Wrap(core.CodeGeneral, "commit task add", err)
	}
	if c.Globals.JSON {
		return c.jsonOut(task)
	}
	fmt.Fprintf(c.Out, "added task %s: %s\n", task.ID, task.Title)
	return nil
}

func cmdTaskUpdate(c *Context, args []string) error {
	fs := flag.NewFlagSet("task update", flag.ContinueOnError)
	id := fs.String("id", "", "task id")
	title := fs.String("title", "", "new title")
	phase := fs.String("phase", "", "new phase")
	owner := fs.String("owner", "", "new owner: agent|human")
	status := fs.String("status", "", "new status: todo|doing|blocked|done")
	expected := fs.String("expected-revision", "", "require this current revision")
	var acceptance, depends stringList
	fs.Var(&acceptance, "acceptance", "replace acceptance criteria (repeatable)")
	fs.Var(&depends, "depends-on", "replace dependencies (repeatable)")
	usage := "Usage: lem task update <id> [--title T] [--phase P] [--owner agent|human] [--status S]\n  [--acceptance ...] [--depends-on ...]\n"
	ok, err := parseFlags(c, fs, args, usage)
	if !ok {
		return err
	}
	if *id == "" && fs.NArg() == 1 {
		*id = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "task update accepts at most one positional task id")
	}
	if *id == "" {
		return core.Errorf(core.CodeUsage, "task update requires a task id")
	}
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	if len(set) == 0 {
		return core.Errorf(core.CodeUsage, "task update requires at least one field flag")
	}
	if set["status"] && !schema.ValidTaskStatus(*status) {
		return core.Errorf(core.CodeUsage, "invalid --status %q", *status)
	}
	if set["owner"] && !schema.ValidOwner(*owner) {
		return core.Errorf(core.CodeUsage, "invalid --owner %q", *owner)
	}

	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	lock, err := core.AcquireLock(l.Dir, c.Globals.LockTimeout)
	if err != nil {
		return err
	}
	defer func() { _ = lock.Release() }()
	if err := core.EnsureClean(l); err != nil {
		return err
	}
	if err := core.CheckExpectedRevision(l, *expected); err != nil {
		return err
	}

	tasks, err := l.LoadTasks()
	if err != nil {
		return err
	}
	task := tasks.Find(*id)
	if task == nil {
		return core.Errorf(core.CodeNotFound, "task %s not found", *id)
	}
	fromStatus := task.Status
	var changed []string
	if set["title"] {
		task.Title = *title
		changed = append(changed, "title")
	}
	if set["phase"] {
		task.Phase = *phase
		changed = append(changed, "phase")
	}
	if set["owner"] {
		task.Owner = *owner
		changed = append(changed, "owner")
	}
	if set["status"] {
		task.Status = *status
		changed = append(changed, "status")
	}
	if set["acceptance"] {
		task.Acceptance = acceptance
		changed = append(changed, "acceptance")
	}
	if set["depends-on"] {
		task.DependsOn = depends
		changed = append(changed, "depends_on")
	}
	if len(changed) == 0 {
		return core.Errorf(core.CodeUsage, "task update requires at least one mutable field flag")
	}
	task.UpdatedAt = l.NowUTC()
	if err := l.SaveTasks(tasks); err != nil {
		return err
	}
	if _, err := l.AppendEvent("task.update", task.ID, "", "", map[string]any{
		"id": task.ID, "from": fromStatus, "to": task.Status,
		"changed": changed, "title": task.Title,
	}); err != nil {
		return err
	}
	if _, err := l.WriteState(true); err != nil {
		return err
	}
	if _, _, err := l.Commit("task: update " + task.ID); err != nil {
		return core.Wrap(core.CodeGeneral, "commit task update", err)
	}
	if c.Globals.JSON {
		return c.jsonOut(task)
	}
	fmt.Fprintf(c.Out, "updated task %s (%s)\n", task.ID, task.Status)
	return nil
}

func cmdTaskList(c *Context, args []string) error {
	fs := flag.NewFlagSet("task list", flag.ContinueOnError)
	status := fs.String("status", "", "filter by status")
	phase := fs.String("phase", "", "filter by phase")
	owner := fs.String("owner", "", "filter by owner")
	ok, err := parseFlags(c, fs, args, "Usage: lem task list [--status S] [--phase P] [--owner O]\n")
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "task list takes no positional arguments")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	tasks, err := l.LoadTasks()
	if err != nil {
		return err
	}
	out := []schema.Task{}
	for _, t := range tasks.Tasks {
		if *status != "" && t.Status != *status {
			continue
		}
		if *phase != "" && t.Phase != *phase {
			continue
		}
		if *owner != "" && t.Owner != *owner {
			continue
		}
		out = append(out, t)
	}
	if c.Globals.JSON {
		return c.jsonOut(out)
	}
	if len(out) == 0 {
		fmt.Fprintln(c.Out, "no tasks")
		return nil
	}
	tw := tabwriter.NewWriter(c.Out, 2, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tPHASE\tSTATUS\tOWNER\tTITLE")
	for _, t := range out {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", t.ID, t.Phase, t.Status, t.Owner, t.Title)
	}
	_ = tw.Flush()
	return nil
}
