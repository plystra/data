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
	"sort"
	"strings"
	"time"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/protocol"
)

type listedClosurePackage struct {
	listedPackage
	Standard bool          `json:"Standard"`
	Module   *listedModule `json:"Module"`
}

// DiscoverSourceClosure adds the Go-selected type-only import closure to
// declaration packages without making imported packages eligible Data roots.
func DiscoverSourceClosure(ctx context.Context, projectRoot string, options SourceOptions) (SourceInventory, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	inventory, err := DiscoverDataRoots(ctx, projectRoot, options)
	if err != nil || len(inventory.Packages) == 0 {
		return inventory, err
	}
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return SourceInventory{}, ErrSourceInventory
	}
	beforeMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || len(beforeMod) > maxGoOutput {
		return SourceInventory{}, ErrSourceInventory
	}
	beforeSum, err := readOptionalInput(filepath.Join(root, "go.sum"))
	if err != nil {
		return SourceInventory{}, err
	}
	env := sourceGoEnvironment(options.Offline)
	selected := make(map[string]SourcePackage, len(inventory.Packages))
	for _, pkg := range inventory.Packages {
		selected[pkg.ImportPath] = pkg
	}
	listed := make(map[string]listedClosurePackage)
	for _, pkg := range inventory.Packages {
		args := []string{"list", "-deps", "-e", "-json", "-mod=readonly"}
		if len(options.BuildTags) != 0 {
			args = append(args, "-tags="+strings.Join(options.BuildTags, ","))
		}
		args = append(args, pkg.ImportPath)
		output, err := sourceGoOutput(ctx, root, env, args...)
		if err != nil {
			return SourceInventory{}, fmt.Errorf("%w: list package dependencies: %v", ErrSourceInventory, err)
		}
		decoder := json.NewDecoder(bytes.NewReader(output))
		for {
			var dep listedClosurePackage
			if err := decoder.Decode(&dep); errors.Is(err, io.EOF) {
				break
			} else if err != nil || dep.ImportPath == "" {
				return SourceInventory{}, ErrSourceInventory
			}
			if dep.Standard {
				continue
			}
			if prior, exists := listed[dep.ImportPath]; exists {
				if prior.Dir != dep.Dir {
					return SourceInventory{}, ErrSourceInventory
				}
				continue
			}
			listed[dep.ImportPath] = dep
		}
	}
	paths := make([]string, 0, len(listed))
	for importPath := range listed {
		paths = append(paths, importPath)
	}
	if len(paths) > protocol.MaxSnapshotFiles {
		return SourceInventory{}, ErrSourceInventory
	}
	sort.Strings(paths)
	roots := make(map[string]string, len(paths))
	type moduleLocation struct {
		root   string
		source ModuleSource
	}
	locations := make(map[string]moduleLocation)
	verified := make(map[string]struct {
		root, path, version, checksum string
	})
	for _, importPath := range paths {
		dep := listed[importPath]
		if dep.Module == nil {
			return SourceInventory{}, fmt.Errorf("%w: missing module for %s", ErrSourceInventory, importPath)
		}
		location, exists := locations[dep.Module.Path]
		if !exists {
			moduleRoot, source, err := selectedModuleRoot(ctx, root, env, *dep.Module)
			if err != nil {
				return SourceInventory{}, fmt.Errorf("%w: source module for %s: %v", ErrSourceInventory, importPath, err)
			}
			location = moduleLocation{root: moduleRoot, source: source}
			locations[dep.Module.Path] = location
		}
		if !withinModule(location.root, dep.Dir) {
			return SourceInventory{}, fmt.Errorf("%w: package %s is outside its selected module", ErrSourceInventory, importPath)
		}
		roots[importPath] = location.root
		if _, exists := selected[importPath]; exists {
			continue
		}
		source := location.source
		if source.Origin == "module" || source.Origin == "versioned-replacement" {
			selectedPath, selectedVersion := dep.Module.Path, dep.Module.Version
			if dep.Module.Replace != nil {
				selectedPath, selectedVersion = dep.Module.Replace.Path, dep.Module.Replace.Version
			}
			checksum := source.Checksum
			if checksum == "" {
				checksum, err = selectedChecksum(ctx, root, env, selectedPath, selectedVersion)
				if err != nil {
					return SourceInventory{}, err
				}
			}
			identity := selectedPath + "@" + selectedVersion
			if _, exists := verified[identity]; !exists {
				if err := verifyModuleSource(location.root, selectedPath, selectedVersion, checksum); err != nil {
					return SourceInventory{}, err
				}
				verified[identity] = struct{ root, path, version, checksum string }{location.root, selectedPath, selectedVersion, checksum}
			}
			source.Checksum = checksum
			location.source = source
			locations[dep.Module.Path] = location
		}
		candidate := packageCandidate{moduleRoot: location.root, directory: dep.Dir, importPath: importPath, module: source}
		pkg, _, err := loadCandidate(ctx, root, candidate, env, options.BuildTags)
		if err != nil || len(pkg.Files) == 0 {
			return SourceInventory{}, fmt.Errorf("%w: load support package %s: %v", ErrSourceInventory, importPath, err)
		}
		pkg.RootEligibility = analyzer.RootSupport
		selected[importPath] = SourcePackage{Package: pkg, Module: source}
	}
	inventory.Packages = inventory.Packages[:0]
	for _, pkg := range selected {
		inventory.Packages = append(inventory.Packages, pkg)
	}
	sort.Slice(inventory.Packages, func(i, j int) bool { return inventory.Packages[i].ImportPath < inventory.Packages[j].ImportPath })
	if err := validateSourcePackages(inventory.Packages, roots); err != nil {
		return SourceInventory{}, fmt.Errorf("%w: support snapshot: %v", ErrSourceInventory, err)
	}
	for _, module := range verified {
		if err := verifyModuleSource(module.root, module.path, module.version, module.checksum); err != nil {
			return SourceInventory{}, err
		}
	}
	afterMod, modErr := os.ReadFile(filepath.Join(root, "go.mod"))
	afterSum, sumErr := readOptionalInput(filepath.Join(root, "go.sum"))
	if modErr != nil || sumErr != nil || !bytes.Equal(beforeMod, afterMod) || !bytes.Equal(beforeSum, afterSum) {
		return SourceInventory{}, ErrSourceInventory
	}
	return inventory, nil
}

func withinModule(moduleRoot, directory string) bool {
	if moduleRoot == "" || directory == "" {
		return false
	}
	relative, err := filepath.Rel(moduleRoot, directory)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}
