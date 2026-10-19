package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/plystra/data/compilerselection"
	"github.com/plystra/data/project"
	"github.com/plystra/data/protocol"
)

const configName = "plystra.data.yaml"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		usage(stdout)
		return 0
	}
	if len(args) != 0 && args[0] == "analyze" {
		return runAnalyze(args[1:], stdout, stderr)
	}
	if len(args) < 2 || args[0] != "inspect" || (args[1] != "assignments" && args[1] != "compiler" && args[1] != "sources" && args[1] != "inputs") {
		usage(stderr)
		return 2
	}
	kind := args[1]
	flags := flag.NewFlagSet("inspect "+kind, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	projectDir := flags.String("project", ".", "Project directory")
	format := flags.String("format", "text", "text or json")
	var offline *bool
	var tags *string
	var closure *bool
	if kind == "compiler" || kind == "sources" || kind == "inputs" {
		offline = flags.Bool("offline", false, "use only verified cached modules")
	}
	if kind == "sources" || kind == "inputs" {
		tags = flags.String("tags", "", "comma-separated Go build tags")
		if kind == "sources" {
			closure = flags.Bool("closure", false, "include type-only support packages")
		}
	}
	if err := flags.Parse(args[2:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, "plystra-data: invalid inspect arguments")
		usage(stderr)
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
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(stderr, "plystra-data: Project directory is unavailable")
		return 2
	}
	if kind == "compiler" {
		return inspectCompiler(root, *format, *offline, stdout, stderr)
	}
	if kind == "sources" {
		var selectedTags []string
		if *tags != "" {
			selectedTags = strings.Split(*tags, ",")
		}
		return inspectSources(root, *format, *offline, selectedTags, *closure, stdout, stderr)
	}
	if kind == "inputs" {
		var selectedTags []string
		if *tags != "" {
			selectedTags = strings.Split(*tags, ",")
		}
		return inspectInputs(root, *format, *offline, selectedTags, stdout, stderr)
	}
	return inspectAssignments(root, *format, stdout, stderr)
}

func runAnalyze(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("analyze", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	projectDir := flags.String("project", ".", "Project directory")
	format := flags.String("format", "text", "text or json")
	offline := flags.Bool("offline", false, "use only cached modules and toolchains")
	tags := flags.String("tags", "", "comma-separated Go build tags")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			usage(stdout)
			return 0
		}
		fmt.Fprintln(stderr, "plystra-data: invalid analyze arguments")
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
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		fmt.Fprintln(stderr, "plystra-data: Project directory is unavailable")
		return 2
	}
	var selectedTags []string
	if *tags != "" {
		selectedTags = strings.Split(*tags, ",")
	}
	result, err := project.AnalyzeCurrentProject(context.Background(), root, project.SourceOptions{Offline: *offline, BuildTags: selectedTags})
	if err != nil {
		switch {
		case errors.Is(err, compilerselection.ErrInvalidSelection):
			fmt.Fprintln(stderr, "plystra-data: selected Data compiler failed integrity validation")
		case errors.Is(err, compilerselection.ErrUnavailable):
			fmt.Fprintln(stderr, "plystra-data: selected Data compiler is unavailable")
		case errors.Is(err, compilerselection.ErrConcurrentChange):
			fmt.Fprintln(stderr, "plystra-data: Project module inputs changed during compiler selection")
		case errors.Is(err, project.ErrSourceInventory):
			fmt.Fprintln(stderr, "plystra-data: selected Data source or type input is unavailable or changed")
		case errors.Is(err, project.ErrCompilerResponse):
			fmt.Fprintln(stderr, "plystra-data: selected Data compiler returned an invalid response")
		default:
			fmt.Fprintln(stderr, "plystra-data: selected Data compiler invocation failed")
		}
		return 2
	}
	if *format == "json" {
		if err := json.NewEncoder(stdout).Encode(struct {
			Schema   string                    `json:"schema"`
			Compiler protocol.CompilerIdentity `json:"compiler"`
			Response protocol.AnalyzeResponse  `json:"response"`
		}{Schema: "plystra.data-analysis/v1", Compiler: result.Compiler, Response: result.Response}); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write analysis output")
			return 1
		}
	} else if result.Response.Status == protocol.StatusSucceeded {
		if _, err := fmt.Fprintf(stdout, "Analyzed %d Data members.\n", len(result.Response.Output.Roots)); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write analysis output")
			return 1
		}
		for _, root := range result.Response.Output.Roots {
			if _, err := fmt.Fprintln(stdout, root.MemberID); err != nil {
				fmt.Fprintln(stderr, "plystra-data: cannot write analysis output")
				return 1
			}
		}
	} else {
		for _, diagnostic := range result.Response.Diagnostics {
			if _, err := fmt.Fprintf(stderr, "%s: %s\n", diagnostic.Code, diagnostic.Message); err != nil {
				return 1
			}
		}
	}
	if result.Response.Status != protocol.StatusSucceeded {
		return 2
	}
	return 0
}

func inspectInputs(root, format string, offline bool, tags []string, stdout, stderr io.Writer) int {
	private, err := os.MkdirTemp("", "plystra-data-inputs-")
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot create a private input snapshot")
		return 2
	}
	defer os.RemoveAll(private)
	staged, err := project.StageAnalyzeInput(context.Background(), root, filepath.Join(private, "snapshot"), project.SourceOptions{Offline: offline, BuildTags: tags})
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot obtain the selected source and standard-library type inputs")
		return 2
	}
	digest, err := protocol.SnapshotManifestDigest(staged.Manifest)
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot validate the private input snapshot")
		return 2
	}
	if err := os.RemoveAll(private); err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot remove the private input snapshot")
		return 2
	}
	if format == "json" {
		if err := json.NewEncoder(stdout).Encode(struct {
			Schema          string                            `json:"schema"`
			Build           protocol.BuildContext             `json:"build"`
			GoToolchain     string                            `json:"go_toolchain"`
			SnapshotDigest  string                            `json:"snapshot_digest"`
			SourcePackages  int                               `json:"source_packages"`
			StandardExports []protocol.SnapshotStandardExport `json:"standard_exports"`
		}{
			Schema: "plystra.data-input-inspection/v1", Build: staged.Build, GoToolchain: staged.GoToolchain,
			SnapshotDigest: digest, SourcePackages: len(staged.Manifest.Packages), StandardExports: staged.Manifest.StandardExports,
		}); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
			return 1
		}
		return 0
	}
	if _, err := fmt.Fprintf(stdout, "%s  %s  %d source packages  %d standard exports\n", staged.GoToolchain, digest, len(staged.Manifest.Packages), len(staged.Manifest.StandardExports)); err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
		return 1
	}
	return 0
}

func inspectAssignments(root, format string, stdout, stderr io.Writer) int {
	file, err := os.Open(filepath.Join(root, configName))
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot read "+configName+" in the selected Project")
		return 2
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		fmt.Fprintln(stderr, "plystra-data: "+configName+" must be a regular file")
		return 2
	}
	content, err := io.ReadAll(io.LimitReader(file, project.MaxAssignmentDocumentBytes+1))
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot read "+configName)
		return 2
	}
	assignments, err := project.ParseAssignments(content)
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: "+err.Error())
		return 2
	}
	if format == "json" {
		if err := json.NewEncoder(stdout).Encode(struct {
			Schema  string               `json:"schema"`
			Members []project.Assignment `json:"members"`
		}{Schema: "plystra.data-assignments/v1", Members: assignments}); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
			return 1
		}
		return 0
	}
	if len(assignments) == 0 {
		if _, err := fmt.Fprintln(stdout, "No active Data members."); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
			return 1
		}
		return 0
	}
	for _, assignment := range assignments {
		var err error
		if assignment.Access == "" {
			_, err = fmt.Fprintf(stdout, "%s  %s\n", assignment.MemberID, assignment.Resource)
		} else {
			_, err = fmt.Fprintf(stdout, "%s  %s  %s\n", assignment.MemberID, assignment.Resource, assignment.Access)
		}
		if err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
			return 1
		}
	}
	return 0
}

func inspectCompiler(root, format string, offline bool, stdout, stderr io.Writer) int {
	selection, err := compilerselection.Resolve(context.Background(), root, compilerselection.Options{Offline: offline})
	if err != nil {
		switch {
		case errors.Is(err, compilerselection.ErrConcurrentChange):
			fmt.Fprintln(stderr, "plystra-data: Project go.mod or go.sum changed during inspection; retry")
		case errors.Is(err, compilerselection.ErrInvalidSelection):
			fmt.Fprintln(stderr, "plystra-data: selected Data compiler source or manifest failed integrity validation")
		default:
			fmt.Fprintln(stderr, "plystra-data: selected Data compiler is unavailable; check Project go.sum, dependencies, proxy, and module cache")
		}
		return 2
	}
	if format == "json" {
		if err := json.NewEncoder(stdout).Encode(struct {
			Schema              string `json:"schema"`
			ModulePath          string `json:"module_path"`
			ModuleVersion       string `json:"module_version"`
			ModuleChecksum      string `json:"module_checksum"`
			ManifestDigest      string `json:"manifest_digest"`
			CommandImportPath   string `json:"command_import_path"`
			AnalyzeProtocol     string `json:"analyze_protocol"`
			EmitProtocol        string `json:"emit_protocol"`
			DeclarationLanguage string `json:"declaration_language"`
			GoToolchain         string `json:"go_toolchain"`
		}{
			Schema: "plystra.data-compiler-inspection/v1", ModulePath: protocol.ModulePath,
			ModuleVersion: selection.ModuleVersion, ModuleChecksum: selection.ModuleChecksum,
			ManifestDigest:      selection.ManifestDigest,
			CommandImportPath:   selection.Manifest.CommandImportPath,
			AnalyzeProtocol:     selection.Manifest.AnalyzeProtocol,
			EmitProtocol:        selection.Manifest.EmitProtocol,
			DeclarationLanguage: selection.Manifest.DeclarationLanguage,
			GoToolchain:         selection.GoToolchain,
		}); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
			return 1
		}
		return 0
	}
	if _, err := fmt.Fprintf(stdout, "%s@%s  %s  %s\n", protocol.ModulePath, selection.ModuleVersion, selection.ModuleChecksum, selection.ManifestDigest); err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
		return 1
	}
	return 0
}

func inspectSources(root, format string, offline bool, tags []string, closure bool, stdout, stderr io.Writer) int {
	options := project.SourceOptions{Offline: offline, BuildTags: tags}
	var inventory project.SourceInventory
	var err error
	if closure {
		inventory, err = project.DiscoverSourceClosure(context.Background(), root, options)
	} else {
		inventory, err = project.DiscoverDataRoots(context.Background(), root, options)
	}
	if err != nil {
		fmt.Fprintln(stderr, "plystra-data: cannot discover Data source packages in the selected Project module graph")
		return 2
	}
	type sourceFile struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
		Bytes  int    `json:"bytes"`
	}
	type sourcePackage struct {
		ImportPath  string               `json:"import_path"`
		Eligibility string               `json:"root_eligibility"`
		Module      project.ModuleSource `json:"module"`
		Files       []sourceFile         `json:"files"`
	}
	packages := make([]sourcePackage, 0, len(inventory.Packages))
	for _, pkg := range inventory.Packages {
		entry := sourcePackage{ImportPath: pkg.ImportPath, Eligibility: string(pkg.RootEligibility), Module: pkg.Module, Files: make([]sourceFile, 0, len(pkg.Files))}
		for _, file := range pkg.Files {
			sum := sha256.Sum256(file.Content)
			entry.Files = append(entry.Files, sourceFile{Path: file.Path, Digest: "sha256:" + hex.EncodeToString(sum[:]), Bytes: len(file.Content)})
		}
		packages = append(packages, entry)
	}
	if format == "json" {
		if err := json.NewEncoder(stdout).Encode(struct {
			Schema   string                `json:"schema"`
			Build    protocol.BuildContext `json:"build"`
			Packages []sourcePackage       `json:"packages"`
		}{Schema: "plystra.data-source-inventory/v1", Build: inventory.Build, Packages: packages}); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
			return 1
		}
		return 0
	}
	if len(packages) == 0 {
		if _, err := fmt.Fprintln(stdout, "No Data declaration packages."); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
			return 1
		}
		return 0
	}
	for _, pkg := range packages {
		identity := pkg.Module.Path
		if pkg.Module.Version != "" {
			identity += "@" + pkg.Module.Version
		}
		if _, err := fmt.Fprintf(stdout, "%s (%s, %s, %s)\n", pkg.ImportPath, identity, pkg.Module.Origin, pkg.Eligibility); err != nil {
			fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
			return 1
		}
		for _, file := range pkg.Files {
			if _, err := fmt.Fprintf(stdout, "  %s  %d  %s\n", file.Path, file.Bytes, file.Digest); err != nil {
				fmt.Fprintln(stderr, "plystra-data: cannot write inspection output")
				return 1
			}
		}
	}
	return 0
}

func usage(output io.Writer) {
	fmt.Fprintln(output, "usage: plystra-data analyze [--project DIR] [--format text|json] [--tags TAG[,TAG...]] [--offline]")
	fmt.Fprintln(output, "usage: plystra-data inspect assignments [--project DIR] [--format text|json]")
	fmt.Fprintln(output, "       plystra-data inspect compiler [--project DIR] [--format text|json] [--offline]")
	fmt.Fprintln(output, "       plystra-data inspect sources [--project DIR] [--format text|json] [--tags TAG[,TAG...]] [--offline] [--closure]")
	fmt.Fprintln(output, "       plystra-data inspect inputs [--project DIR] [--format text|json] [--tags TAG[,TAG...]] [--offline]")
}
