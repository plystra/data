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

	"github.com/plystra/data/project"
	"github.com/plystra/data/protocol"
	"golang.org/x/mod/module"
	modzip "golang.org/x/mod/zip"
)

func TestPlanRejectsInvalidArgumentsAndExplainsHelp(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		args []string
		want string
	}{
		{[]string{"--format", "yaml"}, "expected --format"},
		{[]string{"extra"}, "no positional arguments"},
		{[]string{"--unknown"}, "invalid plan arguments"},
		{[]string{"--project", filepath.Join(root, "missing")}, "Project directory is unavailable"},
		{[]string{"--project", root}, "cannot read plystra.data.yaml"},
	} {
		var stdout, stderr bytes.Buffer
		if code := run(append([]string{"plan"}, test.args...), &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.want) {
			t.Fatalf("plan %v = %d, %q, %q", test.args, code, stdout.String(), stderr.String())
		}
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"plan", "--help"}, &stdout, &stderr); code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "plystra-data plan [--project DIR]") {
		t.Fatalf("plan help = %d, %q, %q", code, stdout.String(), stderr.String())
	}
}

func TestPublicPlanUsesSelectedCompilerWithoutInstallingOrTesting(t *testing.T) {
	const version = "v0.0.0-plan-test"
	moduleRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	proxy := filepath.Join(t.TempDir(), "proxy")
	versionRoot := filepath.Join(proxy, "github.com", "plystra", "data", "@v")
	goMod, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(moduleRoot, "plystra-data-compiler.json"))
	if err != nil {
		t.Fatal(err)
	}
	writeAnalyzeFixture(t, filepath.Join(versionRoot, version+".mod"), goMod)
	writeAnalyzeFixture(t, filepath.Join(versionRoot, version+".info"), []byte(fmt.Sprintf("{\"Version\":%q,\"Time\":\"2026-10-25T00:00:00Z\"}\n", version)))
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
	root := filepath.Join(t.TempDir(), "project")
	writeAnalyzeFixture(t, filepath.Join(root, "go.mod"), []byte("module example.com/app\ngo 1.26\nrequire github.com/plystra/data "+version+"\n"))
	writeAnalyzeFixture(t, filepath.Join(root, "plystra.yaml"), []byte("project: app\n"))
	writeAnalyzeFixture(t, filepath.Join(root, "plystra.data.yaml"), []byte("data:\n  members:\n    accounts.user/v1: {resource: database.primary, access: access.generated}\n"))
	writeAnalyzeFixture(t, filepath.Join(root, "access", "resource.go"), []byte(`package access
import "context"
//plystra:resource accounts.access/v1
type Resource interface {
 Read(context.Context, Request) ([]Row, error)
 Register(context.Context, CreateRequest) (CreateRow, error)
}
type Request struct{}
type Row struct{ ID int64 }
type CreateRequest struct{ ID int64 }
type CreateRow struct{ ID int64 }
`))
	writeAnalyzeFixture(t, filepath.Join(root, "model", "member.go"), []byte(`package model
import("example.com/app/access";"github.com/plystra/data/declaration")
//plystra:data accounts.user/v1
var Accounts=declaration.Member[access.Resource]{
 Namespace:"accounts",
 Tables:[]declaration.Table{{ID:"users",Columns:[]declaration.Column{{ID:"id",Type:"int64"}}}},
 Queries:[]declaration.Query{
  {ID:"read",Method:"Read",Cardinality:"many",Operation:"select",Table:declaration.Ref{Symbol:"users"},Projection:[]declaration.Projection{{Column:"id"}},Results:[]declaration.ResultField{{ID:"id",GoField:"ID",Source:"id",Type:"int64"}}},
  {ID:"create",Operation:"insert",Table:declaration.Ref{Symbol:"users"},Parameters:[]declaration.Parameter{{ID:"id",Type:"int64"}},Assignments:[]declaration.Assignment{{Column:"id",Value:declaration.Value{Set:true,Kind:"int64",Parameter:"id"}}},Returning:[]declaration.Projection{{Column:"id"}},Results:[]declaration.ResultField{{ID:"id",Source:"id",Type:"int64"}}},
 },
 Plans:[]declaration.Plan{{ID:"register",Method:"Register",Parameters:[]declaration.Parameter{{ID:"id",GoField:"ID",Type:"int64"}},Statements:[]declaration.PlanStatement{{ID:"create",Query:declaration.Ref{Symbol:"create"},Cardinality:"exactly_one",Affected:declaration.AffectedRows{Set:true,Min:1,Max:1},Bindings:[]declaration.PlanBinding{{Parameter:"id",FromParameter:"id"}}}},Outputs:[]declaration.PlanOutput{{Statement:"create",Result:"id",GoField:"ID"}}}},
}
//plystra:data dormant.member/v1
var Dormant=declaration.Member[declaration.NoAccess]{Namespace:"dormant"}
func init(){panic("authored initialization must not execute")}
`))
	// Invalid test syntax makes any attempted application test compilation fail.
	writeAnalyzeFixture(t, filepath.Join(root, "model", "member_test.go"), []byte("package model\nfunc TestBusiness( {\n"))
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	binDir := t.TempDir()
	commandPath := filepath.Join(binDir, "plystra-data"+suffix)
	runAnalyzeGo(t, moduleRoot, 45*time.Second, "build", "-mod=readonly", "-buildvcs=false", "-o", commandPath, "./cmd/plystra-data")
	coreSource := filepath.Join(t.TempDir(), "main.go")
	writeAnalyzeFixture(t, coreSource, []byte(`package main
import("fmt";"os";"path/filepath";"strings")
func main(){
 if strings.HasPrefix(filepath.Base(os.Args[0]),"plystra-data-compiler") {os.WriteFile(os.Getenv("PLAN_COMPILER_TRAP"),[]byte("invoked"),0600);os.Exit(90)}
 if strings.Join(os.Args[1:]," ")!="inspect resources --configured --format json" || os.Getenv("PLYSTRA_ENV")!="" || os.Getenv("PLYSTRA_CONFIG")!="" || os.Getenv("GOPROXY")!="off" {os.Exit(91)}
 calls:=os.Getenv("PLAN_CORE_CALLS")
 prior,_:=os.ReadFile(calls)
 if os.WriteFile(calls,append(prior,'x'),0600)!=nil {os.Exit(92)}
 result:=os.Getenv("PLAN_CORE_RESULT")
 if len(prior)>0 && os.Getenv("PLAN_CORE_CHANGED")!="" {result=os.Getenv("PLAN_CORE_CHANGED")}
 fmt.Println(result)
}
`))
	corePath := filepath.Join(binDir, "plystra"+suffix)
	runAnalyzeGo(t, moduleRoot, 45*time.Second, "build", "-buildvcs=false", "-o", corePath, coreSource)
	coreBinary, err := os.ReadFile(corePath)
	if err != nil {
		t.Fatal(err)
	}
	writeAnalyzeFixture(t, filepath.Join(binDir, "plystra-data-compiler"+suffix), coreBinary)
	if err := os.Chmod(filepath.Join(binDir, "plystra-data-compiler"+suffix), 0o755); err != nil {
		t.Fatal(err)
	}
	cache, err := exec.Command("go", "env", "GOMODCACHE").Output()
	if err != nil {
		t.Fatal(err)
	}
	fileURL := func(path string) string {
		path = filepath.ToSlash(path)
		if runtime.GOOS == "windows" {
			path = "/" + path
		}
		return (&url.URL{Scheme: "file", Path: path}).String()
	}
	t.Setenv("GOENV", "off")
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "module-cache"))
	t.Setenv("GOPROXY", fileURL(proxy)+","+fileURL(filepath.Join(strings.TrimSpace(string(cache)), "cache", "download")))
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GONOPROXY", "none")
	runAnalyzeGo(t, root, 45*time.Second, "mod", "download", "all")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("PLYSTRA_ENV", "must-not-be-inherited")
	t.Setenv("PLYSTRA_CONFIG", "must-not-be-inherited.yaml")
	t.Setenv("PLAN_CORE_RESULT", coreResourceFixture)
	trap := filepath.Join(t.TempDir(), "compiler-trap")
	calls := filepath.Join(t.TempDir(), "core-calls")
	t.Setenv("PLAN_COMPILER_TRAP", trap)
	t.Setenv("PLAN_CORE_CALLS", calls)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	invoke := func(format string, want int, message string) []byte {
		t.Helper()
		writeAnalyzeFixture(t, calls, nil)
		before := snapshotAnalyzeProject(t, root)
		var stdout, stderr bytes.Buffer
		command := exec.CommandContext(ctx, commandPath, "plan", "--project", root, "--format", format, "--offline")
		command.Stdout, command.Stderr = &stdout, &stderr
		err := command.Run()
		code := 0
		if err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("public plan process: %v", err)
			}
			code = exit.ExitCode()
		}
		if code != want || (want == 0 && stderr.Len() != 0) || (want != 0 && (stdout.Len() != 0 || !strings.Contains(stderr.String(), message))) {
			t.Fatalf("public plan = %d, want %d, stdout = %q, stderr = %q", code, want, stdout.String(), stderr.String())
		}
		if !reflect.DeepEqual(before, snapshotAnalyzeProject(t, root)) {
			t.Fatal("public plan changed Project files")
		}
		if _, err := os.Stat(trap); !os.IsNotExist(err) {
			t.Fatal("ambient compiler was invoked")
		}
		return stdout.Bytes()
	}
	var result struct {
		Schema string `json:"schema"`
		project.EmitPlan
	}
	if err := json.Unmarshal(invoke("json", 0, ""), &result); err != nil {
		t.Fatal(err)
	}
	checksum := ""
	sumPath := filepath.Join(root, "go.sum")
	originalSums, err := os.ReadFile(sumPath)
	if err != nil {
		t.Fatal(err)
	}
	checksumLine := ""
	for _, line := range strings.Split(string(originalSums), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == protocol.ModulePath && fields[1] == version {
			checksum, checksumLine = fields[2], line+"\n"
		}
	}
	manifestSum := sha256.Sum256(manifest)
	if result.Schema != "plystra.data-emit-plan/v1" || result.Request.Compiler.ModuleVersion != version ||
		checksum == "" || result.Request.Compiler.ModuleChecksum != checksum ||
		result.Request.Compiler.ManifestDigest != "sha256:"+hex.EncodeToString(manifestSum[:]) ||
		result.Request.Compiler.GoToolchain != runtime.Version() || result.Response.Status != protocol.StatusSucceeded ||
		protocol.ValidateEmitResponse(result.Request, result.Response) != nil || len(result.Request.Assignments) != 1 {
		t.Fatalf("unvalidated or unpinned public plan: %#v", result)
	}
	foundProvider, foundPlan := false, false
	for _, artifact := range result.Response.Output.Artifacts {
		if strings.Contains(artifact.Path, "dormant") {
			t.Fatalf("dormant member emitted: %s", artifact.Path)
		}
		if strings.HasSuffix(artifact.Path, "/providers/accounts.user/v1/provider.go") {
			foundProvider = bytes.Contains(artifact.Bytes, []byte("//plystra:implements-resource accounts.access/v1")) &&
				bytes.Contains(artifact.Bytes, []byte("func (provider *Provider) Register(")) &&
				bytes.Contains(artifact.Bytes, []byte(`db.InputArgument(db.ValueKind("int64"), "id")`))
		}
		foundPlan = foundPlan || (artifact.Plan != nil && artifact.Plan.ID == "register")
	}
	if !foundProvider || !foundPlan {
		t.Fatal("public plan lacks real typed SELECT/atomic plan provider or metadata")
	}
	writeAnalyzeFixture(t, sumPath, bytes.Replace(originalSums, []byte(checksumLine), nil, 1))
	invoke("json", 2, "selected Data compiler is unavailable")
	writeAnalyzeFixture(t, sumPath, originalSums)
	t.Setenv("PLAN_CORE_CHANGED", strings.Replace(coreResourceFixture, `"source_line":5`, `"source_line":6`, 1))
	invoke("json", 2, "configured Resources changed during planning")
}
