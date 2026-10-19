package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/protocol"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func TestPublicAnalyzeUsesSelectedArchivedCompiler(t *testing.T) {
	const version = "v0.0.0-test"
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	proxy := filepath.Join(t.TempDir(), "proxy")
	versionRoot := filepath.Join(proxy, "github.com", "plystra", "data", "@v")
	if err := os.MkdirAll(versionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	goMod, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(moduleRoot, "plystra-data-compiler.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeAnalyzeFixture(t, filepath.Join(versionRoot, version+".mod"), goMod)
	writeAnalyzeFixture(t, filepath.Join(versionRoot, version+".info"), []byte(fmt.Sprintf("{\"Version\":%q,\"Time\":\"2026-10-19T00:00:00Z\"}\n", version)))
	archive, err := os.Create(filepath.Join(versionRoot, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	if err := modzip.CreateFromDir(archive, module.Version{Path: protocol.ModulePath, Version: version}, moduleRoot); err != nil {
		archive.Close()
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}

	projectRoot := filepath.Join(t.TempDir(), "project")
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "go.mod"), []byte("module example.com/app\ngo 1.26\nrequire github.com/plystra/data "+version+"\n"))
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "plystra.yaml"), []byte("project: app\n"))
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "model", "member.go"), []byte(`package model

import "github.com/plystra/data/declaration"

//plystra:data audit.log/v1
var Audit = declaration.Member[declaration.NoAccess]{Namespace: "audit"}

var Broken = missingAuthoredName
`))
	commandPath := filepath.Join(t.TempDir(), "plystra-data")
	if runtime.GOOS == "windows" {
		commandPath += ".exe"
	}
	runAnalyzeGo(t, moduleRoot, 90*time.Second, "build", "-o", commandPath, "./cmd/plystra-data")

	proxyPath := filepath.ToSlash(proxy)
	if runtime.GOOS == "windows" {
		proxyPath = "/" + proxyPath
	}
	proxyURL := (&url.URL{Scheme: "file", Path: proxyPath}).String()
	t.Setenv("GOENV", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "module-cache"))
	t.Setenv("GOPROXY", proxyURL+",https://proxy.golang.org")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GONOPROXY", "none")
	runAnalyzeGo(t, projectRoot, 2*time.Minute, "mod", "download", "all")
	before := snapshotAnalyzeProject(t, projectRoot)
	checksum := ""
	checksumLine := ""
	for _, line := range strings.Split(string(before["go.sum"]), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == protocol.ModulePath && fields[1] == version {
			checksum = fields[2]
			checksumLine = line + "\n"
		}
	}
	if !strings.HasPrefix(checksum, "h1:") {
		t.Fatalf("Project go.sum lacks the selected full-module checksum: %q", checksum)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "go.sum"), bytes.Replace(before["go.sum"], []byte(checksumLine), nil, 1))
	unanchored := snapshotAnalyzeProject(t, projectRoot)
	missingSum := exec.CommandContext(ctx, commandPath, "analyze", "--project", projectRoot, "--format", "json", "--offline")
	var missingStdout, missingStderr bytes.Buffer
	missingSum.Stdout, missingSum.Stderr = &missingStdout, &missingStderr
	err = missingSum.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || missingStdout.Len() != 0 ||
		!strings.Contains(missingStderr.String(), "selected Data compiler is unavailable") ||
		!reflect.DeepEqual(unanchored, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatalf("unanchored analyze = %v, stdout = %q, stderr = %q", err, missingStdout.String(), missingStderr.String())
	}
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "go.sum"), before["go.sum"])
	command := exec.CommandContext(ctx, commandPath, "analyze", "--project", projectRoot, "--format", "json", "--offline")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || ctx.Err() != nil || stderr.Len() != 0 {
		t.Fatalf("public analyze = %v, context = %v, stdout = %q, stderr = %q", err, ctx.Err(), stdout.String(), stderr.String())
	}
	var output struct {
		Schema   string                    `json:"schema"`
		Compiler protocol.CompilerIdentity `json:"compiler"`
		Response protocol.AnalyzeResponse  `json:"response"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatalf("decode public response: %v: %q", err, stdout.String())
	}
	manifestSum := sha256.Sum256(manifest)
	if output.Schema != "plystra.data-analysis/v1" || output.Compiler.ModulePath != protocol.ModulePath ||
		output.Compiler.ModuleVersion != version || output.Compiler.ModuleChecksum != checksum ||
		output.Compiler.ManifestDigest != "sha256:"+hex.EncodeToString(manifestSum[:]) ||
		output.Compiler.CommandImportPath != protocol.CommandImportPath ||
		output.Response.Schema != protocol.AnalyzeSchema || output.Response.Status != protocol.StatusRejected ||
		len(output.Response.Diagnostics) == 0 || output.Response.RequestID == "" || output.Response.InputDigest == "" {
		t.Fatalf("public analysis response = %#v", output)
	}
	foundAuthoredError := false
	for _, diagnostic := range output.Response.Diagnostics {
		foundAuthoredError = foundAuthoredError || diagnostic.Code == analyzer.CodeTypeCheck && diagnostic.FilePath == "model/member.go"
	}
	if !foundAuthoredError {
		t.Fatalf("analysis did not report the authored type error: %#v", output.Response.Diagnostics)
	}
	if !reflect.DeepEqual(before, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatal("analyze changed Project files")
	}
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "model", "member.go"), []byte(`package model

import "github.com/plystra/data/declaration"

//plystra:data audit.log/v1
var Audit = declaration.Member[declaration.NoAccess]{Namespace: "audit"}
`))
	validBefore := snapshotAnalyzeProject(t, projectRoot)
	stdout.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, commandPath, "analyze", "--project", projectRoot, "--format", "json", "--offline")
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("valid public analyze = %v, stderr = %q", err, stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Response.Status != protocol.StatusSucceeded ||
		len(output.Response.Diagnostics) != 0 || len(output.Response.Output.Roots) != 1 ||
		output.Response.Output.Roots[0].MemberID != "audit.log/v1" {
		t.Fatalf("valid analysis = %#v, %v", output.Response, err)
	}
	if !reflect.DeepEqual(validBefore, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatal("valid analyze changed Project files")
	}
}

func writeAnalyzeFixture(t *testing.T, path string, content []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}

func runAnalyzeGo(t *testing.T, dir string, timeout time.Duration, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = dir
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func snapshotAnalyzeProject(t *testing.T, root string) map[string][]byte {
	t.Helper()
	files := make(map[string][]byte)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files[filepath.ToSlash(relative)], err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
