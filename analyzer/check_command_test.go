package analyzer_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/plystra/data/protocol"
)

func TestCheckCompilerCommandAnalyzeAndEmit(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	binary := filepath.Join(t.TempDir(), "data-compiler.exe")
	build := exec.CommandContext(ctx, "go", "build", "-buildvcs=false", "-o", binary, "../cmd/plystra-data-compiler")
	build.Env = append(os.Environ(), "GOWORK=off", "GOPROXY=off", "GOSUMDB=off")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build compiler command: %v, %s", err, output)
	}
	identity := protocol.CompilerIdentity{
		ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)),
		ManifestDigest: "sha256:" + strings.Repeat("2", 64), CommandImportPath: protocol.CommandImportPath,
		AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26",
	}
	buildContext := protocol.BuildContext{GOOS: runtime.GOOS, GOARCH: runtime.GOARCH}
	for _, invalid := range []bool{false, true} {
		body := checkSource
		if invalid {
			body = strings.Replace(body, "Int64: 0", `Parameter: "input"`, 1)
		}
		snapshot := sourceSnapshot(body)
		manifest, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), snapshot)
		if err != nil {
			t.Fatal(err)
		}
		digest, err := protocol.AnalyzeInputDigest(manifest, buildContext)
		if err != nil {
			t.Fatal(err)
		}
		request := protocol.AnalyzeRequest{Schema: protocol.AnalyzeSchema, Phase: protocol.AnalyzePhase, RequestID: "check-analyze", Compiler: identity,
			Build: buildContext, Limits: snapshot.Limits, InputDigest: digest, Snapshot: manifest}
		var analyzed protocol.AnalyzeResponse
		runCheckCompiler(t, ctx, binary, request, &analyzed)
		if err := protocol.ValidateAnalyzeResponse(request, analyzed); err != nil {
			t.Fatal(err)
		}
		if invalid {
			if analyzed.Status != protocol.StatusRejected || analyzed.Output.Valid {
				t.Fatal("public command accepted parameterized CHECK")
			}
			continue
		}
		if analyzed.Status != protocol.StatusSucceeded || !analyzed.Output.Valid {
			t.Fatalf("public CHECK analyze: %v", analyzed.Diagnostics)
		}
		emit := protocol.EmitRequest{Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "check-emit", Compiler: identity,
			Build: buildContext, Analyze: analyzed.Output, AnalyzeDigest: analyzed.Output.Digest, FrozenModelDigest: "sha256:" + strings.Repeat("3", 64),
			Assignments: []protocol.EmitAssignment{{MemberID: "checks.records/v1", Resource: "database.primary", ResourceContract: "data.database/v1",
				Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "dataaccess/database.primary"}},
		}
		emit.InputDigest, err = protocol.EmitInputDigest(emit)
		if err != nil {
			t.Fatal(err)
		}
		var emitted protocol.EmitResponse
		runCheckCompiler(t, ctx, binary, emit, &emitted)
		if emitted.Status != protocol.StatusSucceeded || protocol.ValidateEmitResponse(emit, emitted) != nil {
			t.Fatalf("public CHECK emit: %v", emitted.Diagnostics)
		}
		var schema, initial, added bool
		for _, artifact := range emitted.Output.Artifacts {
			text := string(artifact.Bytes)
			if strings.HasSuffix(artifact.Path, "schema/schema.sql") && strings.Contains(text, `CONSTRAINT "valid" CHECK`) {
				schema = true
			}
			if artifact.Migration != nil {
				switch artifact.Migration.Identity.Name {
				case "initial":
					initial = strings.Contains(text, `CONSTRAINT "positive" CHECK ("amount" > 0)`)
				case "strict":
					added = strings.Contains(text, `ALTER TABLE "checks"."records" ADD CONSTRAINT "required" CHECK ("amount" IS NOT NULL);`)
				}
			}
		}
		if !schema || !initial || !added {
			t.Fatalf("public artifacts omitted CHECK: schema=%t initial=%t added=%t", schema, initial, added)
		}
	}
}

func runCheckCompiler(t *testing.T, ctx context.Context, binary string, request, response any) {
	t.Helper()
	var input, stdout, stderr bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, binary)
	command.Stdin, command.Stdout, command.Stderr = &input, &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("compiler command: %v, stderr=%s", err, &stderr)
	}
	if err := protocol.ReadFrame(&stdout, response); err != nil || stdout.Len() != 0 {
		t.Fatalf("compiler response frame: %v, trailing=%d", err, stdout.Len())
	}
}
