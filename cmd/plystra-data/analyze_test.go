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
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "access", "resource.go"), []byte(`package access
import "context"

//plystra:resource accounts.access/v1
type Resource interface { Read(context.Context, Request) ([]Row, error) }
type Request struct{}
type Row struct { ID int64 }
`))
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "model", "member.go"), []byte(`package model

import (
	"example.com/app/access"
	"github.com/plystra/data/declaration"
)

//plystra:data audit.log/v1
var Audit = declaration.Member[declaration.NoAccess]{Namespace: "audit"}

//plystra:data accounts.reader/v1
var Reader = declaration.Member[access.Resource]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "entries", Columns: []declaration.Column{{ID: "id", Type: "int64"}}}},
	Queries: []declaration.Query{{
		ID: "read", Method: "Read", Cardinality: "many", Operation: "select", Table: declaration.Ref{Symbol: "entries"},
		Projection: []declaration.Projection{{Column: "id"}},
		Results: []declaration.ResultField{{ID: "id", GoField: "ID", Source: "id", Type: "int64"}},
	}},
}
`))
	accessBefore := snapshotAnalyzeProject(t, projectRoot)
	stdout.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, commandPath, "analyze", "--project", projectRoot, "--format", "json", "--offline")
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("access public analyze = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Response.Status != protocol.StatusSucceeded ||
		len(output.Response.Diagnostics) != 0 || len(output.Response.Output.Roots) != 2 {
		t.Fatalf("access analysis = %#v, %v", output.Response, err)
	}
	var foundAccess, foundNoAccess bool
	for _, root := range output.Response.Output.Roots {
		switch root.MemberID {
		case "accounts.reader/v1":
			foundAccess = true
			if root.AccessPackage != "example.com/app/access" || root.AccessType != "Resource" ||
				root.AccessID != "accounts.access/v1" || root.MigrationOnly || len(root.QueryMethods) != 1 ||
				root.QueryMethods[0].Name != "Read" || root.QueryMethods[0].RequestType != "Request" ||
				root.QueryMethods[0].RowType != "Row" || root.QueryMethods[0].Cardinality != "many" {
				t.Fatalf("access root = %#v", root)
			}
		case "audit.log/v1":
			foundNoAccess = true
			if !root.MigrationOnly {
				t.Fatalf("NoAccess root = %#v", root)
			}
		default:
			t.Fatalf("unexpected root = %#v", root)
		}
	}
	if !foundAccess || !foundNoAccess {
		t.Fatalf("missing access or NoAccess root: %#v", output.Response.Output.Roots)
	}
	if !reflect.DeepEqual(accessBefore, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatal("access analyze changed Project files")
	}
	assignmentPath := filepath.Join(projectRoot, configName)
	writeAnalyzeFixture(t, assignmentPath, []byte("data:\n  members:\n    accounts.reader/v1: {resource: database.primary, access: accounts.reader}\n    audit.log/v1: {resource: database.primary}\n"))
	assignedBefore := snapshotAnalyzeProject(t, projectRoot)
	stdout.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, commandPath, "check", "assignments", "--project", projectRoot, "--format", "json", "--offline")
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("public assignment check = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
	var checked struct {
		Schema  string `json:"schema"`
		Members []struct {
			MemberID      string `json:"member_id"`
			Resource      string `json:"resource"`
			Access        string `json:"access"`
			AccessPackage string `json:"access_package"`
			AccessID      string `json:"access_id"`
			ModelDigest   string `json:"model_digest"`
		} `json:"members"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &checked); err != nil || checked.Schema != "plystra.data-activation-check/v1" ||
		len(checked.Members) != 2 || checked.Members[0].MemberID != "accounts.reader/v1" ||
		checked.Members[0].Resource != "database.primary" || checked.Members[0].Access != "accounts.reader" ||
		checked.Members[0].AccessPackage != "example.com/app/access" || checked.Members[0].AccessID != "accounts.access/v1" ||
		!strings.HasPrefix(checked.Members[0].ModelDigest, "sha256:") || checked.Members[1].MemberID != "audit.log/v1" ||
		checked.Members[1].Access != "" || !reflect.DeepEqual(assignedBefore, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatalf("assignment check = %#v, %v", checked, err)
	}
	stubDir := t.TempDir()
	stubSource := filepath.Join(stubDir, "main.go")
	writeAnalyzeFixture(t, stubSource, []byte("package main\nimport (\"fmt\"; \"os\")\nfunc main() { if os.Getenv(\"GOPROXY\") != \"off\" || os.Getenv(\"GOSUMDB\") != \"off\" || os.Getenv(\"PLYSTRA_ENV\") != \"\" || os.Getenv(\"PLYSTRA_CONFIG\") != \"\" { os.Exit(3) }; fmt.Print(os.Getenv(\"CORE_RESULT\")) }\n"))
	stubPath := filepath.Join(stubDir, "plystra")
	if runtime.GOOS == "windows" {
		stubPath += ".exe"
	}
	runAnalyzeGo(t, moduleRoot, 90*time.Second, "build", "-o", stubPath, stubSource)
	t.Setenv("PATH", stubDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	selectedResources := strings.ReplaceAll(coreResourceFixture, "access.generated", "accounts.reader")
	t.Setenv("CORE_RESULT", selectedResources)
	t.Setenv("PLYSTRA_ENV", "production")
	t.Setenv("PLYSTRA_CONFIG", "deploy/other.yaml")
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "plystra.yaml"), []byte("resources:\n  instances:\n    database.primary:\n      use: github.com/plystra/data/postgres.New\n    accounts.reader:\n      use: example.com/app/dataaccess.New\n"))
	resourcesBefore := snapshotAnalyzeProject(t, projectRoot)
	stdout.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, commandPath, "check", "resources", "--project", projectRoot, "--format", "json", "--offline")
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("public Resource check = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
	var resourceCheck struct {
		Schema    string                      `json:"schema"`
		Members   []struct{ MemberID string } `json:"members"`
		Databases []struct {
			Resource string `json:"resource"`
			Provider string `json:"provider"`
			Backend  string `json:"backend"`
		} `json:"databases"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &resourceCheck); err != nil || resourceCheck.Schema != "plystra.data-resource-check/v1" ||
		len(resourceCheck.Members) != 2 || len(resourceCheck.Databases) != 1 ||
		resourceCheck.Databases[0].Resource != "database.primary" || resourceCheck.Databases[0].Provider != "github.com/plystra/data/postgres.New" || resourceCheck.Databases[0].Backend != "postgres/v1" ||
		!reflect.DeepEqual(resourcesBefore, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatalf("Resource check = %#v, %v", resourceCheck, err)
	}
	t.Setenv("CORE_RESULT", strings.Replace(selectedResources, `"configured_only"`, `"resolved"`, 1))
	stdout.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, commandPath, "check", "resources", "--project", projectRoot, "--offline")
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err == nil || stdout.Len() != 0 || !strings.Contains(stderr.String(), "compatible Core CLI Resource inspection is unavailable") ||
		!reflect.DeepEqual(resourcesBefore, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatalf("invalid Core result = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
	writeAnalyzeFixture(t, assignmentPath, []byte("data:\n  members:\n    audit.log/v1: {resource: database.primary}\n"))
	dormantBefore := snapshotAnalyzeProject(t, projectRoot)
	stdout.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, commandPath, "check", "assignments", "--project", projectRoot, "--offline")
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 || stdout.String() != "Validated 1 active Data members.\naudit.log/v1  database.primary\n" ||
		!reflect.DeepEqual(dormantBefore, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatalf("dormant assignment check = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
	writeAnalyzeFixture(t, assignmentPath, []byte("data:\n  members:\n    accounts.reader/v1: {resource: database.primary}\n"))
	invalidBefore := snapshotAnalyzeProject(t, projectRoot)
	stdout.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, commandPath, "check", "assignments", "--project", projectRoot, "--offline")
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), "requires an accepted access contract and instance") ||
		!reflect.DeepEqual(invalidBefore, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatalf("invalid assignment check = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "access", "resource.go"), []byte(`package access
//plystra:resource accounts.access/v1
type Resource interface { Read() error }
`))
	wrongShapeBefore := snapshotAnalyzeProject(t, projectRoot)
	stdout.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, commandPath, "analyze", "--project", projectRoot, "--format", "json", "--offline")
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || stderr.Len() != 0 {
		t.Fatalf("wrong access shape analyze = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Response.Status != protocol.StatusRejected ||
		!reflect.DeepEqual(wrongShapeBefore, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatalf("wrong access shape response = %#v, %v", output.Response, err)
	}
	wrongShapeMismatch := false
	for _, diagnostic := range output.Response.Diagnostics {
		wrongShapeMismatch = wrongShapeMismatch || diagnostic.Code == analyzer.CodeAccessContractMismatch
	}
	if !wrongShapeMismatch {
		t.Fatalf("wrong access shape diagnostic = %#v", output.Response.Diagnostics)
	}
	writeAnalyzeFixture(t, filepath.Join(projectRoot, "access", "resource.go"), []byte(`package access

type Resource interface { Read() error }
`))
	missingDirectiveBefore := snapshotAnalyzeProject(t, projectRoot)
	stdout.Reset()
	stderr.Reset()
	command = exec.CommandContext(ctx, commandPath, "analyze", "--project", projectRoot, "--format", "json", "--offline")
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if exit, ok := err.(*exec.ExitError); !ok || exit.ExitCode() != 2 || stderr.Len() != 0 {
		t.Fatalf("missing Resource directive analyze = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Response.Status != protocol.StatusRejected {
		t.Fatalf("missing Resource directive response = %#v, %v", output.Response, err)
	}
	foundAccessMismatch := false
	for _, diagnostic := range output.Response.Diagnostics {
		foundAccessMismatch = foundAccessMismatch || diagnostic.Code == analyzer.CodeAccessContractMismatch
	}
	if !foundAccessMismatch || !reflect.DeepEqual(missingDirectiveBefore, snapshotAnalyzeProject(t, projectRoot)) {
		t.Fatalf("missing Resource directive was not rejected without Project mutation: %#v", output.Response.Diagnostics)
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
