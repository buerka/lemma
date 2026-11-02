package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/buerka/lemma/internal/core"
	"github.com/buerka/lemma/internal/schema"
)

func cmdRun(c *Context, args []string) error {
	if len(args) == 0 {
		return core.Errorf(core.CodeUsage, "run requires a subcommand: start, finish, status")
	}
	switch args[0] {
	case "start":
		return cmdRunStart(c, args[1:])
	case "finish":
		return cmdRunFinish(c, args[1:])
	case "status":
		return cmdRunStatus(c, args[1:])
	default:
		return core.Errorf(core.CodeUsage, "unknown run subcommand %q", args[0])
	}
}

func cmdRunStart(c *Context, args []string) error {
	fs := flag.NewFlagSet("run start", flag.ContinueOnError)
	taskID := fs.String("task", "", "task id")
	method := fs.String("method", "default", "method label")
	split := fs.String("split", "default", "split label")
	seed := fs.Int("seed", 0, "random seed")
	command := fs.String("command", "", "remote command (default derived from method)")
	runID := fs.String("run-id", "", "explicit run id")
	config := fs.String("config", "", "config file (hashed)")
	data := fs.String("data", "", "data path or URI (hashed)")
	dataSHA := fs.String("data-sha256", "", "data SHA-256 for remote data")
	force := fs.Bool("force", false, "overwrite an existing run spec")
	expected := fs.String("expected-revision", "", "require this current revision")
	var envList, outputList stringList
	fs.Var(&envList, "env", "remote env K=V (repeatable)")
	fs.Var(&outputList, "output", "declared output (repeatable)")
	usage := `Usage: lem run start --task T1 [--method baseline] [--split random] [--seed 0]
  [--command "..."] [--config FILE] [--data PATH|URI] [--data-sha256 HASH]
  [--run-id ID] [--env K=V] [--output metrics.json] [--force]

Writes progress/runs/<run_id>/{run_spec.json,REMOTE_TASK.md,runner.sh,runner.py}
and records a run.start event.
`
	ok, err := parseFlags(c, fs, args, usage)
	if !ok {
		return err
	}
	if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "run start takes no positional arguments")
	}
	env := map[string]string{}
	for _, kv := range envList {
		k, v, found := strings.Cut(kv, "=")
		if !found || k == "" {
			return core.Errorf(core.CodeUsage, "invalid --env %q (expected K=V)", kv)
		}
		env[k] = v
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	res, err := core.StartRun(l, core.StartRunOptions{
		TaskID: *taskID, Method: *method, Split: *split, Seed: *seed,
		Command: *command, RunID: *runID, Config: *config, Data: *data,
		DataSHA256: *dataSHA, Env: env, Outputs: outputList, Force: *force,
		ExpectedRevision: *expected, LockTimeout: c.Globals.LockTimeout,
	})
	if err != nil {
		return err
	}
	if res.AlreadyExists {
		c.ExitCode = 5
		if c.Globals.JSON {
			return c.jsonOut(res)
		}
		fmt.Fprintf(c.Err, "lem: run %s already started; returning existing spec\n", res.Spec.RunID)
	} else if c.Globals.JSON {
		return c.jsonOut(res)
	}
	fmt.Fprintf(c.Out, "started %s\n", res.Spec.RunID)
	fmt.Fprintf(c.Out, "  spec:        %s\n", l.Rel(res.SpecPath))
	fmt.Fprintf(c.Out, "  remote task: %s\n", l.Rel(res.RemoteTaskPath))
	fmt.Fprintf(c.Out, "  host:        %s\n", res.Spec.Host)
	fmt.Fprintf(c.Out, "  command:     %s\n", res.Spec.Command)
	fmt.Fprintln(c.Out, "\nGive REMOTE_TASK.md to the runner subagent. Then run:")
	fmt.Fprintf(c.Out, "  lem run finish %s --result %s\n", res.Spec.RunID, l.Rel(res.Dir)+"/remote_result.json")
	return nil
}

func cmdRunFinish(c *Context, args []string) error {
	fs := flag.NewFlagSet("run finish", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run id")
	result := fs.String("result", "", "path to remote_result.json")
	force := fs.Bool("force", false, "overwrite an existing result")
	expected := fs.String("expected-revision", "", "require this current revision")
	usage := "Usage: lem run finish <run_id> --result <remote_result.json> [--force] [--expected-revision HASH]\n"
	ok, err := parseFlags(c, fs, args, usage)
	if !ok {
		return err
	}
	if *runID == "" && fs.NArg() == 1 {
		*runID = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "run finish accepts at most one positional run id")
	}
	if *runID == "" {
		return core.Errorf(core.CodeUsage, "run finish requires a run id")
	}
	if *result == "" {
		return core.Errorf(core.CodeUsage, "run finish requires --result")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	res, err := core.FinishRun(l, *runID, core.FinishRunOptions{
		ResultPath: *result, Force: *force, ExpectedRevision: *expected,
		LockTimeout: c.Globals.LockTimeout,
	})
	if err != nil {
		return err
	}
	if res.Already {
		c.ExitCode = 5
	}
	if c.Globals.JSON {
		return c.jsonOut(res)
	}
	if res.Already {
		fmt.Fprintf(c.Err, "lem: run %s already finished; returning existing result\n", *runID)
	}
	fmt.Fprintf(c.Out, "finished %s: %s (exit %d)\n", *runID, res.Result.Status, res.Result.ExitCode)
	if len(res.Result.Metrics) > 0 {
		b, _ := json.Marshal(res.Result.Metrics)
		fmt.Fprintf(c.Out, "  metrics: %s\n", string(b))
	}
	fmt.Fprintf(c.Out, "  manifest: %s (%d local files, %d artifacts)\n", l.Rel(res.Dir)+"/manifest.json", len(res.Manifest.Files), len(res.Manifest.Artifacts))
	return nil
}

func cmdRunStatus(c *Context, args []string) error {
	fs := flag.NewFlagSet("run status", flag.ContinueOnError)
	runID := fs.String("run-id", "", "run id")
	ok, err := parseFlags(c, fs, args, "Usage: lem run status [run_id]\n")
	if !ok {
		return err
	}
	if *runID == "" && fs.NArg() == 1 {
		*runID = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return core.Errorf(core.CodeUsage, "run status accepts at most one positional run id")
	}
	l, _, err := c.openLedger()
	if err != nil {
		return err
	}
	if *runID == "" {
		runs, err := core.ListRuns(l)
		if err != nil {
			return err
		}
		if c.Globals.JSON {
			return c.jsonOut(runs)
		}
		if len(runs) == 0 {
			fmt.Fprintln(c.Out, "no runs")
			return nil
		}
		tw := tabwriter.NewWriter(c.Out, 2, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "RUN\tTASK\tSTATUS\tEXIT")
		for _, r := range runs {
			fmt.Fprintf(tw, "%s\t%s\t%s\t%d\n", r.RunID, r.TaskID, r.Status, r.ExitCode)
		}
		_ = tw.Flush()
		return nil
	}
	spec, err := l.LoadRunSpec(*runID)
	if err != nil {
		return err
	}
	result, err := core.LoadRunResult(l, *runID)
	if err != nil {
		return err
	}
	var manifest *schema.Manifest
	if result != nil {
		if m, err := core.ManifestFor(l, *runID); err == nil {
			manifest = m
		}
	}
	status := "running"
	exit := 0
	if result != nil {
		status = result.Status
		exit = result.ExitCode
	}
	if c.Globals.JSON {
		return c.jsonOut(map[string]any{
			"run_id": *runID, "task_id": spec.TaskID, "status": status,
			"exit_code": exit, "spec": spec, "result": result, "manifest": manifest,
		})
	}
	fmt.Fprintf(c.Out, "run:     %s\n", *runID)
	fmt.Fprintf(c.Out, "task:    %s\n", spec.TaskID)
	fmt.Fprintf(c.Out, "status:  %s (exit %d)\n", status, exit)
	fmt.Fprintf(c.Out, "host:    %s\n", spec.Host)
	fmt.Fprintf(c.Out, "command: %s\n", spec.Command)
	fmt.Fprintf(c.Out, "code:    %s %s\n", spec.Code.Commit, spec.Code.PatchHash)
	fmt.Fprintf(c.Out, "config:  %s\n", spec.ConfigHash)
	if result != nil {
		if len(result.Metrics) > 0 {
			keys := make([]string, 0, len(result.Metrics))
			for k := range result.Metrics {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				fmt.Fprintf(c.Out, "metric:  %s=%v\n", k, result.Metrics[k])
			}
		}
		if result.Error != "" {
			fmt.Fprintf(c.Out, "error:   %s\n", result.Error)
		}
		if result.LogsTail != "" {
			tail := strings.ReplaceAll(result.LogsTail, "\n", " | ")
			if len(tail) > 500 {
				tail = tail[:500] + "..."
			}
			fmt.Fprintf(c.Out, "logs:    %s\n", tail)
		}
	}
	if manifest != nil {
		fmt.Fprintf(c.Out, "files:   %d, artifacts: %d\n", len(manifest.Files), len(manifest.Artifacts))
	}
	return nil
}
