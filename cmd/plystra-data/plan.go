package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"

	"github.com/plystra/data/project"
	"github.com/plystra/data/protocol"
)

func runPlan(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	projectDir := flags.String("project", ".", "Project directory")
	format := flags.String("format", "text", "text or json")
	offline := flags.Bool("offline", false, "use only cached modules and toolchains")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, "plystra-data: invalid plan arguments")
		return 2
	}
	if flags.NArg() != 0 || (*format != "text" && *format != "json") {
		fmt.Fprintln(stderr, "plystra-data: expected --format text or json and no positional arguments")
		return 2
	}
	root, err := filepath.Abs(*projectDir)
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: invalid Project directory")
		return 2
	}
	if info, err := os.Stat(root); err != nil || !info.IsDir() {
		fmt.Fprintln(stderr, "plystra-data: Project directory is unavailable")
		return 2
	}
	if _, _, err := loadAssignments(root); err != nil {
		fmt.Fprintln(stderr, "plystra-data: "+err.Error())
		return 2
	}
	ctx := context.Background()
	configured, err := inspectConfiguredResources(ctx, root, *offline)
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: compatible Core CLI Resource inspection is unavailable")
		return 2
	}
	plan, err := project.PlanCurrentProject(ctx, root, project.SourceOptions{Offline: *offline}, configured)
	if err != nil {
		var rejected *project.EmitAnalysisError
		if errors.As(err, &rejected) {
			for _, diagnostic := range rejected.Diagnostics {
				fmt.Fprintf(stderr, "%s: %s\n", diagnostic.Code, diagnostic.Message)
			}
		} else if errors.Is(err, project.ErrEmitInput) || errors.Is(err, project.ErrInvalidActivation) || errors.Is(err, project.ErrDatabaseSelection) || errors.Is(err, project.ErrInvalidAssignments) {
			fmt.Fprintln(stderr, "plystra-data: "+err.Error())
		} else {
			writeAnalyzeError(err, stderr)
		}
		return 2
	}
	current, err := inspectConfiguredResources(ctx, root, *offline)
	if err != nil || !reflect.DeepEqual(configured, current) {
		fmt.Fprintln(stderr, "plystra-data: configured Resources changed during planning; retry")
		return 2
	}
	if plan.Response.Status != protocol.StatusSucceeded {
		for _, diagnostic := range plan.Response.Diagnostics {
			fmt.Fprintf(stderr, "%s: %s\n", diagnostic.Code, diagnostic.Message)
		}
		return 2
	}
	if *format == "json" {
		if err := json.NewEncoder(stdout).Encode(struct {
			Schema string `json:"schema"`
			project.EmitPlan
		}{Schema: "plystra.data-emit-plan/v1", EmitPlan: plan}); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write emit plan")
			return 1
		}
		return 0
	}
	if _, err := fmt.Fprintf(stdout, "Planned %d Data artifacts for %d active members; no files installed.\n", len(plan.Response.Output.Artifacts), len(plan.Request.Assignments)); err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot write emit plan")
		return 1
	}
	for _, artifact := range plan.Response.Output.Artifacts {
		if _, err := fmt.Fprintf(stdout, "%s  %s\n", artifact.Path, artifact.Digest); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write emit plan")
			return 1
		}
	}
	return 0
}
