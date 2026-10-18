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
	if len(args) < 2 || args[0] != "inspect" || (args[1] != "assignments" && args[1] != "compiler") {
		usage(stderr)
		return 2
	}
	kind := args[1]
	flags := flag.NewFlagSet("inspect "+kind, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	projectDir := flags.String("project", ".", "Project directory")
	format := flags.String("format", "text", "text or json")
	var offline *bool
	if kind == "compiler" {
		offline = flags.Bool("offline", false, "use only verified cached modules")
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
	return inspectAssignments(root, *format, stdout, stderr)
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

func usage(output io.Writer) {
	fmt.Fprintln(output, "usage: plystra-data inspect assignments [--project DIR] [--format text|json]")
	fmt.Fprintln(output, "       plystra-data inspect compiler [--project DIR] [--format text|json] [--offline]")
}
