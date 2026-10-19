package project

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/protocol"
)

type StagedAnalyzeInput struct {
	Manifest    protocol.SnapshotManifest
	Build       protocol.BuildContext
	GoToolchain string
}

// StageAnalyzeInput obtains only Go-selected source and standard-library type
// inputs, then verifies their private snapshot before returning its manifest.
func StageAnalyzeInput(ctx context.Context, projectRoot, snapshotRoot string, options SourceOptions) (StagedAnalyzeInput, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return StagedAnalyzeInput{}, ErrSourceInventory
	}
	env := sourceGoEnvironment(options.Offline)
	inputsBefore, err := snapshotModuleInputs(ctx, root, env)
	if err != nil {
		return StagedAnalyzeInput{}, err
	}
	before, err := selectedGoVersion(ctx, root, env)
	if err != nil {
		return StagedAnalyzeInput{}, err
	}
	inventory, err := DiscoverSourceClosure(ctx, root, options)
	if err != nil {
		return StagedAnalyzeInput{}, err
	}
	exports, err := selectedStandardExports(ctx, root, env, inventory.StandardPackages, inventory.Build.BuildTags)
	if err != nil {
		return StagedAnalyzeInput{}, err
	}
	snapshot := analyzer.Snapshot{TargetGOARCH: inventory.Build.GOARCH, StandardExports: exports}
	for _, pkg := range inventory.Packages {
		snapshot.Packages = append(snapshot.Packages, pkg.Package)
	}
	manifest, err := protocol.WriteSnapshot(snapshotRoot, snapshot)
	if err != nil {
		return StagedAnalyzeInput{}, err
	}
	valid := false
	defer func() {
		if !valid && filepath.IsAbs(manifest.Root) && filepath.Clean(manifest.Root) == manifest.Root && manifest.Root == snapshotRoot {
			_ = os.RemoveAll(manifest.Root)
		}
	}()
	if _, err := protocol.LoadSnapshot(manifest); err != nil {
		return StagedAnalyzeInput{}, err
	}
	current, err := DiscoverSourceClosure(ctx, root, options)
	if err != nil || !reflect.DeepEqual(inventory, current) {
		return StagedAnalyzeInput{}, ErrSourceInventory
	}
	inputsAfter, err := snapshotModuleInputs(ctx, root, env)
	if err != nil || !reflect.DeepEqual(inputsBefore, inputsAfter) {
		return StagedAnalyzeInput{}, ErrSourceInventory
	}
	after, err := selectedGoVersion(ctx, root, env)
	if err != nil || before != after {
		return StagedAnalyzeInput{}, ErrSourceInventory
	}
	valid = true
	return StagedAnalyzeInput{Manifest: manifest, Build: inventory.Build, GoToolchain: before}, nil
}

type moduleInputs struct {
	GoMod     []byte
	GoSum     []byte
	WorkPath  string
	GoWork    []byte
	GoWorkSum []byte
}

func snapshotModuleInputs(ctx context.Context, root string, env []string) (moduleInputs, error) {
	mod, err := readSourceFile(filepath.Join(root, "go.mod"), maxGoOutput)
	if err != nil {
		return moduleInputs{}, err
	}
	sum, err := readOptionalInput(filepath.Join(root, "go.sum"))
	if err != nil {
		return moduleInputs{}, err
	}
	workOutput, err := sourceGoOutput(ctx, root, env, "env", "GOWORK")
	if err != nil {
		return moduleInputs{}, ErrSourceInventory
	}
	state := moduleInputs{GoMod: mod, GoSum: sum, WorkPath: strings.TrimSpace(string(workOutput))}
	if state.WorkPath != "" && state.WorkPath != "off" {
		if !filepath.IsAbs(state.WorkPath) {
			return moduleInputs{}, ErrSourceInventory
		}
		state.GoWork, err = readSourceFile(state.WorkPath, maxGoOutput)
		if err != nil {
			return moduleInputs{}, err
		}
		state.GoWorkSum, err = readOptionalInput(state.WorkPath + ".sum")
		if err != nil {
			return moduleInputs{}, err
		}
	}
	return state, nil
}

func selectedGoVersion(ctx context.Context, root string, env []string) (string, error) {
	output, err := sourceGoOutput(ctx, root, env, "version")
	if err != nil {
		return "", ErrSourceInventory
	}
	parts := strings.Fields(string(output))
	if len(parts) < 3 || parts[0] != "go" || parts[1] != "version" || !strings.HasPrefix(parts[2], "go1.") {
		return "", ErrSourceInventory
	}
	return parts[2], nil
}

func selectedStandardExports(ctx context.Context, root string, env []string, paths, tags []string) ([]analyzer.StandardExport, error) {
	if len(paths) > protocol.MaxStandardExports {
		return nil, ErrSourceInventory
	}
	wanted := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if path == "" || path == "unsafe" {
			return nil, ErrSourceInventory
		}
		if _, exists := wanted[path]; exists {
			return nil, ErrSourceInventory
		}
		wanted[path] = struct{}{}
	}
	seen := make(map[string]struct{}, len(paths))
	exports := make([]analyzer.StandardExport, 0, len(paths))
	total := 0
	for start := 0; start < len(paths); start += 16 {
		end := min(start+16, len(paths))
		args := []string{"list", "-mod=readonly", "-export", "-json"}
		if len(tags) != 0 {
			args = append(args, "-tags="+strings.Join(tags, ","))
		}
		args = append(args, paths[start:end]...)
		output, err := sourceGoOutput(ctx, root, env, args...)
		if err != nil {
			return nil, fmt.Errorf("%w: selected standard export is unavailable", ErrSourceInventory)
		}
		decoder := json.NewDecoder(bytes.NewReader(output))
		for {
			var item struct {
				ImportPath string `json:"ImportPath"`
				Export     string `json:"Export"`
				Standard   bool   `json:"Standard"`
				Incomplete bool   `json:"Incomplete"`
				Error      any    `json:"Error"`
			}
			if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
				break
			} else if err != nil || !item.Standard || item.Incomplete || item.Error != nil || item.Export == "" {
				return nil, ErrSourceInventory
			}
			if _, expected := wanted[item.ImportPath]; !expected {
				return nil, ErrSourceInventory
			}
			if _, duplicate := seen[item.ImportPath]; duplicate {
				return nil, ErrSourceInventory
			}
			seen[item.ImportPath] = struct{}{}
			content, err := readSourceFile(item.Export, protocol.MaxStandardFileBytes)
			if err != nil || len(content) == 0 {
				return nil, ErrSourceInventory
			}
			total += len(content)
			if total > protocol.MaxStandardBytes {
				return nil, ErrSourceInventory
			}
			exports = append(exports, analyzer.StandardExport{ImportPath: item.ImportPath, Content: content})
		}
	}
	if len(seen) != len(wanted) {
		return nil, ErrSourceInventory
	}
	return exports, nil
}
