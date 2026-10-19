package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/plystra/data/project"
	"github.com/plystra/data/protocol"
	"golang.org/x/mod/modfile"
)

const maxCoreInspectionBytes = 1 << 20

func runCheckResources(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("check resources", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	projectDir := flags.String("project", ".", "Project directory")
	format := flags.String("format", "text", "text or json")
	offline := flags.Bool("offline", false, "use only cached modules and toolchains")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, "plystra-data: invalid check resources arguments")
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
	assignments, originalAssignments, err := loadAssignments(root)
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: "+err.Error())
		return 2
	}
	ctx := context.Background()
	analysis, err := project.AnalyzeCurrentProject(ctx, root, project.SourceOptions{Offline: *offline})
	if err != nil {
		writeAnalyzeError(err, stderr)
		return 2
	}
	if analysis.Response.Status != protocol.StatusSucceeded {
		for _, diagnostic := range analysis.Response.Diagnostics {
			fmt.Fprintf(stderr, "%s: %s\n", diagnostic.Code, diagnostic.Message)
		}
		return 2
	}
	active, err := project.ValidateActivation(assignments, analysis.Response.Output.Roots)
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: "+err.Error())
		return 2
	}
	modulePath, goMod, err := readProjectModule(root)
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: Project module changed or is invalid")
		return 2
	}
	configured, err := inspectConfiguredResources(ctx, root, *offline)
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: compatible Core CLI Resource inspection is unavailable")
		return 2
	}
	databases, err := project.ValidateDatabaseSelections(active, modulePath, configured)
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: "+err.Error())
		return 2
	}
	_, currentAssignments, err := loadAssignments(root)
	currentModule, currentMod, modErr := readProjectModule(root)
	if err != nil || modErr != nil || currentModule != modulePath || !bytes.Equal(originalAssignments, currentAssignments) || !bytes.Equal(goMod, currentMod) {
		fmt.Fprintln(stderr, "plystra-data: Project inputs changed during validation; retry")
		return 2
	}
	if *format == "json" {
		if err := json.NewEncoder(stdout).Encode(struct {
			Schema    string                      `json:"schema"`
			Compiler  protocol.CompilerIdentity   `json:"compiler"`
			Members   []project.ActiveMember      `json:"members"`
			Databases []project.DatabaseSelection `json:"databases"`
		}{Schema: "plystra.data-resource-check/v1", Compiler: analysis.Compiler, Members: active, Databases: databases}); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write Resource check output")
			return 1
		}
		return 0
	}
	if _, err := fmt.Fprintf(stdout, "Validated %d active Data members on %d PostgreSQL databases.\n", len(active), len(databases)); err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot write Resource check output")
		return 1
	}
	for _, database := range databases {
		if _, err := fmt.Fprintf(stdout, "%s  %s\n", database.Resource, database.Backend); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write Resource check output")
			return 1
		}
	}
	return 0
}

func readProjectModule(root string) (string, []byte, error) {
	file, err := os.Open(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", nil, err
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, 8<<20+1))
	if err != nil || len(content) > 8<<20 {
		return "", nil, errors.New("invalid go.mod")
	}
	parsed, err := modfile.Parse("go.mod", content, nil)
	if err != nil || parsed.Module == nil || parsed.Module.Mod.Path == "" {
		return "", nil, errors.New("invalid go.mod")
	}
	return parsed.Module.Mod.Path, content, nil
}

type coreSelectionResult struct {
	Schema       string `json:"schema"`
	Operation    string `json:"operation"`
	InvocationID string `json:"invocation_id"`
	Snapshot     struct {
		Selector struct {
			Mode string `json:"mode"`
			Name string `json:"name"`
			Path string `json:"path"`
		} `json:"selector"`
	} `json:"snapshot"`
	Status      string            `json:"status"`
	ExitClass   int               `json:"exit_class"`
	Changes     []json.RawMessage `json:"changes"`
	Diagnostics []json.RawMessage `json:"diagnostics"`
	Recovery    []json.RawMessage `json:"recovery"`
	Effects     struct {
		Observed   []json.RawMessage `json:"observed"`
		Planned    []json.RawMessage `json:"planned"`
		Skipped    []json.RawMessage `json:"skipped"`
		Unverified []json.RawMessage `json:"unverified"`
	} `json:"effects"`
	Support []json.RawMessage `json:"support"`
	Payload struct {
		Schema     string                       `json:"schema"`
		ModulePath string                       `json:"module_path"`
		Scope      string                       `json:"scope"`
		Instances  []project.ConfiguredResource `json:"instances"`
	} `json:"payload"`
	Continuation json.RawMessage `json:"continuation"`
}

func inspectConfiguredResources(ctx context.Context, root string, offline bool) (project.ConfiguredResources, error) {
	bounded, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	command := exec.CommandContext(bounded, "plystra", "inspect", "resources", "--configured", "--format", "json")
	command.WaitDelay = 5 * time.Second
	command.Dir = root
	command.Env = coreSelectionEnvironment(offline)
	stdout := &limitedCoreOutput{limit: maxCoreInspectionBytes}
	stderr := &limitedCoreOutput{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return project.ConfiguredResources{}, err
	}
	return parseCoreResourceSelections(stdout.Bytes())
}

func coreSelectionEnvironment(offline bool) []string {
	environment := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		switch strings.ToUpper(key) {
		case "PLYSTRA_ENV", "PLYSTRA_CONFIG":
			continue
		case "GOPROXY", "GOSUMDB":
			if offline {
				continue
			}
		}
		environment = append(environment, entry)
	}
	if offline {
		environment = append(environment, "GOPROXY=off", "GOSUMDB=off")
	}
	return environment
}

func parseCoreResourceSelections(content []byte) (project.ConfiguredResources, error) {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	var result coreSelectionResult
	if err := decoder.Decode(&result); err != nil || decoder.Decode(new(any)) != io.EOF ||
		result.Schema != "plystra.result/v1" || result.Operation != "inspect.resources" || result.InvocationID == "" ||
		result.Status != "success" || result.ExitClass != 0 || result.Snapshot.Selector.Mode != "default" ||
		result.Snapshot.Selector.Name != "" || result.Snapshot.Selector.Path != "" ||
		result.Payload.Schema != "plystra.resource-selections/v1" || result.Payload.Scope != "configured_only" || result.Payload.Instances == nil ||
		result.Changes == nil || len(result.Changes) != 0 || result.Diagnostics == nil || len(result.Diagnostics) != 0 ||
		result.Recovery == nil || len(result.Recovery) != 0 || result.Support == nil || len(result.Support) != 0 ||
		result.Effects.Observed == nil || len(result.Effects.Observed) != 0 || result.Effects.Planned == nil || len(result.Effects.Planned) != 0 ||
		result.Effects.Skipped == nil || len(result.Effects.Skipped) != 0 || result.Effects.Unverified == nil || len(result.Effects.Unverified) != 0 ||
		string(result.Continuation) != "null" {
		return project.ConfiguredResources{}, errors.New("invalid Core CLI Resource inspection")
	}
	return project.ConfiguredResources{ModulePath: result.Payload.ModulePath, Instances: result.Payload.Instances}, nil
}

type limitedCoreOutput struct {
	bytes.Buffer
	limit int
}

func (output *limitedCoreOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > output.limit {
		return 0, io.ErrShortWrite
	}
	return output.Buffer.Write(data)
}
