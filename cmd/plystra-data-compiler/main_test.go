package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/protocol"
)

func TestCompilerCommandEmitsOnlyOneStagedFrame(t *testing.T) {
	model, err := (declaration.Member[struct{}]{
		Namespace: "accounts",
		Tables: []declaration.Table{
			{ID: "accounts", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString}, {ID: "region", Type: declaration.TypeString}, {ID: "team-id", Type: declaration.TypeInt64}, {ID: "revision", Type: declaration.TypeInt64}}, Constraints: []declaration.Constraint{{ID: "accounts-pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"id"}}}},
			{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "region", Type: declaration.TypeString}, {ID: "amount", Type: declaration.TypeFloat64}}},
		},
		Queries: []declaration.Query{{
			ID: "delete-account", Operation: declaration.OperationDelete, Table: declaration.Ref{Symbol: "accounts"},
			Parameters: []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}},
			Predicate:  declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}},
			Returning:  []declaration.Projection{{Column: "id", Alias: "account-id"}},
			Results:    []declaration.ResultField{{ID: "id", Source: "account-id", Type: declaration.TypeInt64}},
		}, {
			ID: "upsert-account", Operation: declaration.OperationInsert, Table: declaration.Ref{Symbol: "accounts"},
			Parameters:  []declaration.Parameter{{ID: "id", Type: declaration.TypeInt64}, {ID: "email", Type: declaration.TypeString}},
			Assignments: []declaration.Assignment{{Column: "id", Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "id"}}, {Column: "email", Value: declaration.Value{Kind: declaration.TypeString, Set: true, Parameter: "email"}}},
			Conflict:    declaration.Conflict{Constraint: "accounts-pk", Action: declaration.ConflictDoUpdate, UpdateColumns: []string{"email"}},
			Returning:   []declaration.Projection{{Column: "id"}}, Results: []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}},
		}, {
			ID: "revise-account", Operation: declaration.OperationUpdate, Table: declaration.Ref{Symbol: "accounts"},
			Parameters:  []declaration.Parameter{{ID: "expected", Type: declaration.TypeInt64}},
			Assignments: []declaration.Assignment{{Column: "revision", Increment: true}},
			Predicate:   declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "revision"}, Value: declaration.Value{Kind: declaration.TypeInt64, Set: true, Parameter: "expected"}},
		}, {
			ID: "count-by-region", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			GroupBy:    []declaration.ColumnRef{{Column: "region"}},
			Projection: []declaration.Projection{{Column: "region"}, {Aggregate: declaration.AggregateCount, Column: "id", Alias: "total"}},
			Having:     declaration.Predicate{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Int64(1)},
			Ordering:   []declaration.Ordering{{Column: "total", Direction: declaration.OrderingDescending}, {Column: "region", Direction: declaration.OrderingAscending}},
			Results:    []declaration.ResultField{{ID: "region", Source: "region", Type: declaration.TypeString}, {ID: "total", Source: "total", Type: declaration.TypeInt64}},
		}, {
			ID: "count-by-team-region", Operation: declaration.OperationSelect, Table: declaration.Ref{Symbol: "accounts"},
			Joins: []declaration.Join{{Kind: declaration.JoinLeft, Table: declaration.Ref{Symbol: "teams"}, On: declaration.JoinCondition{
				Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "accounts"}, Column: "team-id"}, Right: declaration.ColumnRef{Table: declaration.Ref{Symbol: "teams"}, Column: "id"},
			}}},
			GroupBy:    []declaration.ColumnRef{{Table: declaration.Ref{Symbol: "teams"}, Column: "region"}},
			Projection: []declaration.Projection{{Table: declaration.Ref{Symbol: "teams"}, Column: "region", Alias: "zone"}, {Aggregate: declaration.AggregateCount, Table: declaration.Ref{Symbol: "teams"}, Column: "id", Alias: "total"}, {Aggregate: declaration.AggregateMax, Table: declaration.Ref{Symbol: "teams"}, Column: "id", Alias: "maximum"}, {Aggregate: declaration.AggregateSum, Table: declaration.Ref{Symbol: "teams"}, Column: "amount", Alias: "sum"}},
			Results:    []declaration.ResultField{{ID: "zone", Source: "zone", Type: declaration.TypeString, Nullable: true}, {ID: "total", Source: "total", Type: declaration.TypeInt64}, {ID: "maximum", Source: "maximum", Type: declaration.TypeInt64, Nullable: true}, {ID: "sum", Source: "sum", Type: declaration.TypeFloat64, Nullable: true}},
			Ordering:   []declaration.Ordering{{Column: "zone", Direction: declaration.OrderingAscending}},
			HasLimit:   true, Limit: 2, HasOffset: true, Offset: 1,
		}},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	roots := []analyzer.Root{{MemberID: "accounts.user/v1", Model: model, MigrationOnly: true}}
	analyzeDigest, err := analyzer.ResultDigest(roots, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "public-command",
		Compiler: protocol.CompilerIdentity{ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)), ManifestDigest: "sha256:" + strings.Repeat("2", 64), CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26"},
		Build:    protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"}, Analyze: protocol.AnalyzeOutput{Roots: roots, Digest: analyzeDigest, Valid: true}, AnalyzeDigest: analyzeDigest,
		FrozenModelDigest: "sha256:" + strings.Repeat("3", 64),
		Assignments:       []protocol.EmitAssignment{{MemberID: "accounts.user/v1", Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "generated/data/database.primary"}},
	}
	request.InputDigest, err = protocol.EmitInputDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	var input bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".")
	command.Env = append(os.Environ(), "GOWORK=off")
	command.Stdin = &input
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil {
		t.Fatalf("compiler command failed: %v; stderr=%s", err, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("compiler wrote stderr: %q", stderr.String())
	}
	var response protocol.EmitResponse
	if err := protocol.ReadFrame(&stdout, &response); err != nil {
		t.Fatal(err)
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil || stdout.Len() != 0 || len(response.Output.Artifacts) != 7 {
		t.Fatalf("invalid command response: %v, bytes=%d, artifacts=%d", err, stdout.Len(), len(response.Output.Artifacts))
	}
	foundUpsert := false
	foundHaving := false
	foundGroupedJoin := false
	foundIncrement := false
	for _, artifact := range response.Output.Artifacts {
		if strings.HasSuffix(artifact.Path, "/delete-account.sql") && !bytes.Contains(artifact.Bytes, []byte(`RETURNING "id" AS "account-id";`)) {
			t.Fatalf("query artifact does not contain typed RETURNING: %q", artifact.Bytes)
		}
		if strings.HasSuffix(artifact.Path, "/upsert-account.sql") {
			foundUpsert = true
			if artifact.Query == nil || artifact.Query.Conflict == nil || len(artifact.Query.Conflict.UpdateColumns) != 1 || artifact.Query.Conflict.UpdateColumns[0] != "email" || !bytes.Contains(artifact.Bytes, []byte(`DO UPDATE SET "email" = EXCLUDED."email" RETURNING "id";`)) {
				t.Fatalf("query artifact does not contain conflict update: %#v", artifact)
			}
		}
		if strings.HasSuffix(artifact.Path, "/revise-account.sql") {
			foundIncrement = true
			if artifact.Query == nil || !bytes.Contains(artifact.Bytes, []byte(`SET "revision" = "revision" + 1 WHERE "revision" = $1;`)) {
				t.Fatalf("query artifact does not contain revision increment: %#v", artifact)
			}
		}
		if strings.HasSuffix(artifact.Path, "/count-by-region.sql") {
			foundHaving = true
			if artifact.Query == nil || artifact.Query.Having == nil || artifact.Query.Having.Left.Column != "total" || !bytes.Contains(artifact.Bytes, []byte(`GROUP BY "region" HAVING COUNT("id") > 1 ORDER BY COUNT("id") DESC, "accounts"."region" ASC;`)) {
				t.Fatalf("query artifact does not contain grouped ordering: %#v", artifact)
			}
		}
		if strings.HasSuffix(artifact.Path, "/count-by-team-region.sql") {
			foundGroupedJoin = true
			if artifact.Query == nil || len(artifact.Query.GroupBy) != 1 || len(artifact.Query.Results) != 4 || artifact.Query.Results[3] != (declaration.ResultField{ID: "sum", Source: "sum", Type: declaration.TypeFloat64, Nullable: true}) || !bytes.Contains(artifact.Bytes, []byte(`MAX("t1"."id") AS "maximum", SUM("t1"."amount") AS "sum"`)) || !bytes.Contains(artifact.Bytes, []byte(`GROUP BY "t1"."region" ORDER BY "t1"."region" ASC LIMIT 2 OFFSET 1;`)) {
				t.Fatalf("query artifact does not contain grouped join: %#v", artifact)
			}
		}
	}
	if !foundUpsert || !foundIncrement || !foundHaving || !foundGroupedJoin {
		t.Fatal("compiler did not stage the expected mutation and grouped queries")
	}
	after, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(before) != len(after) {
		t.Fatal("compiler wrote files in the project directory")
	}
}

func TestCompilerCommandRequiresCompleteSelectResults(t *testing.T) {
	users := declaration.Ref{Symbol: "users"}
	teams := declaration.Ref{Symbol: "teams"}
	regions := declaration.Ref{Member: "geo.region/v1", Symbol: "regions"}
	owner, err := (declaration.Member[struct{}]{
		Namespace: "accounts", Imports: []string{"geo.region/v1"},
		Tables: []declaration.Table{
			{ID: "users", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "team-id", Type: declaration.TypeInt64}, {ID: "region-id", Type: declaration.TypeInt64}}},
			{ID: "teams", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "label", Type: declaration.TypeString}}},
		},
		Queries: []declaration.Query{
			{ID: "local-teams", Operation: declaration.OperationSelect, Table: users,
				Joins:      []declaration.Join{{Kind: declaration.JoinLeft, Table: teams, On: declaration.JoinCondition{Left: declaration.ColumnRef{Table: users, Column: "team-id"}, Right: declaration.ColumnRef{Table: teams, Column: "id"}}}},
				Projection: []declaration.Projection{{Table: users, Column: "id"}, {Table: teams, Column: "label", Alias: "team-label"}},
				Results:    []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}, {ID: "label", Source: "team-label", Type: declaration.TypeString, Nullable: true}},
			},
			{ID: "imported-regions", Operation: declaration.OperationSelect, Table: users,
				Joins:      []declaration.Join{{Kind: declaration.JoinLeft, Table: regions, On: declaration.JoinCondition{Left: declaration.ColumnRef{Table: users, Column: "region-id"}, Right: declaration.ColumnRef{Table: regions, Column: "id"}}}},
				Projection: []declaration.Projection{{Table: users, Column: "id"}, {Table: regions, Column: "name", Alias: "region-name"}},
				Results:    []declaration.ResultField{{ID: "id", Source: "id", Type: declaration.TypeInt64}, {ID: "name", Source: "region-name", Type: declaration.TypeString, Nullable: true}},
			},
		},
	}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	remote, err := (declaration.Member[struct{}]{Namespace: "geo", Tables: []declaration.Table{{ID: "regions", Columns: []declaration.Column{{ID: "id", Type: declaration.TypeInt64}, {ID: "name", Type: declaration.TypeString}}}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: "select-results",
		Compiler:          protocol.CompilerIdentity{ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)), ManifestDigest: "sha256:" + strings.Repeat("2", 64), CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26"},
		Build:             protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"},
		Analyze:           protocol.AnalyzeOutput{Roots: []analyzer.Root{{MemberID: "accounts.user/v1", Model: owner, MigrationOnly: true}, {MemberID: "geo.region/v1", Model: remote, MigrationOnly: true}}, Valid: true},
		FrozenModelDigest: "sha256:" + strings.Repeat("3", 64),
		Assignments: []protocol.EmitAssignment{
			{MemberID: "accounts.user/v1", Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "generated/data/database.primary"},
			{MemberID: "geo.region/v1", Resource: "database.primary", ResourceContract: "data.database/v1", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1", AllowedRoot: "generated/data/database.primary"},
		},
	}
	run := func(t *testing.T, request protocol.EmitRequest) protocol.EmitResponse {
		t.Helper()
		var err error
		request.Analyze.Digest, err = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		request.AnalyzeDigest = request.Analyze.Digest
		request.InputDigest, err = protocol.EmitInputDigest(request)
		if err != nil {
			t.Fatal(err)
		}
		var input bytes.Buffer
		if err := protocol.WriteFrame(&input, request); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		command := exec.CommandContext(ctx, "go", "run", ".")
		command.Env = append(os.Environ(), "GOWORK=off")
		command.Stdin = &input
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		if err := command.Run(); err != nil || stderr.Len() != 0 {
			t.Fatalf("compiler emit failed: %v; stderr=%q", err, stderr.String())
		}
		var response protocol.EmitResponse
		if err := protocol.ReadFrame(&stdout, &response); err != nil || stdout.Len() != 0 {
			t.Fatalf("emit response = %#v, %v; remaining=%d", response, err, stdout.Len())
		}
		if err := protocol.ValidateEmitResponse(request, response); err != nil {
			t.Fatal(err)
		}
		return response
	}
	first := run(t, request)
	second := run(t, request)
	if first.Status != protocol.StatusSucceeded || first.OutputDigest == "" || first.OutputDigest != second.OutputDigest {
		t.Fatalf("unstable SELECT emit response: first=%#v second=%#v", first, second)
	}
	wantSQL := map[string]string{
		"queries/accounts.user/v1/local-teams.sql":      "SELECT \"t0\".\"id\", \"t1\".\"label\" AS \"team-label\" FROM \"accounts\".\"users\" AS \"t0\" LEFT JOIN \"accounts\".\"teams\" AS \"t1\" ON \"t0\".\"team-id\" = \"t1\".\"id\";",
		"queries/accounts.user/v1/imported-regions.sql": "SELECT \"t0\".\"id\", \"t1\".\"name\" AS \"region-name\" FROM \"accounts\".\"users\" AS \"t0\" LEFT JOIN \"geo\".\"regions\" AS \"t1\" ON \"t0\".\"region-id\" = \"t1\".\"id\";",
	}
	for _, artifact := range first.Output.Artifacts {
		path := strings.TrimPrefix(artifact.Path, "generated/data/database.primary/")
		sql, exists := wantSQL[path]
		if !exists {
			continue
		}
		if !bytes.Contains(artifact.Bytes, []byte(sql)) || artifact.Query == nil || len(artifact.Query.Results) != 2 || artifact.Query.Results[1].Nullable != true || artifact.Query.Results[1].Type != declaration.TypeString || artifact.Digest == "" {
			t.Fatalf("invalid typed SELECT artifact: %#v", artifact)
		}
		delete(wantSQL, path)
	}
	if len(wantSQL) != 0 {
		t.Fatalf("missing typed SELECT artifacts: %v", wantSQL)
	}
	for _, test := range []struct {
		name             string
		query            string
		keep             int
		wrongNullability bool
		code             analyzer.DiagnosticCode
	}{
		{name: "missing local mapping", query: "local-teams", code: analyzer.CodeEmitInput},
		{name: "missing imported mapping", query: "imported-regions", code: analyzer.CodeEmitInput},
		{name: "partial imported mapping", query: "imported-regions", keep: 1, code: analyzer.CodeEmitInput},
		{name: "non-null imported left join mapping", query: "imported-regions", keep: 2, wrongNullability: true, code: analyzer.CodeEmitBackend},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := request
			invalid.Analyze.Roots = append([]analyzer.Root(nil), request.Analyze.Roots...)
			model := invalid.Analyze.Roots[0].Model
			model.Queries = append([]declaration.Query(nil), model.Queries...)
			for index := range model.Queries {
				if model.Queries[index].ID == test.query {
					model.Queries[index].Results = model.Queries[index].Results[:test.keep]
					if test.wrongNullability {
						model.Queries[index].Results = append([]declaration.ResultField(nil), model.Queries[index].Results...)
						model.Queries[index].Results[1].Nullable = false
					}
				}
			}
			invalid.Analyze.Roots[0].Model = model
			response := run(t, invalid)
			if response.Status != protocol.StatusRejected || response.OutputDigest != "" || len(response.Output.Artifacts) != 0 || len(response.Diagnostics) != 1 || response.Diagnostics[0].Code != test.code {
				t.Fatalf("invalid SELECT mapping response = %#v", response)
			}
		})
	}
}

func TestCompilerCommandAnalyzesOneBoundedFrame(t *testing.T) {
	snapshot := analyzer.Snapshot{}
	manifest, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	build := protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"}
	digest, err := protocol.AnalyzeInputDigest(manifest, build)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.AnalyzeRequest{
		Schema: protocol.AnalyzeSchema, Phase: protocol.AnalyzePhase, RequestID: "public-analyze",
		Compiler: protocol.CompilerIdentity{
			ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test",
			ModuleChecksum:    "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)),
			ManifestDigest:    "sha256:" + strings.Repeat("2", 64),
			CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema,
			EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage,
			GoToolchain: "go1.26",
		},
		Build: build, Limits: snapshot.Limits, InputDigest: digest, Snapshot: manifest,
	}
	var input bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".")
	command.Env = append(os.Environ(), "GOWORK=off")
	command.Stdin = &input
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("compiler analyze failed: %v; stderr=%q", err, stderr.String())
	}
	var response protocol.AnalyzeResponse
	if err := protocol.ReadFrame(&stdout, &response); err != nil || stdout.Len() != 0 {
		t.Fatalf("analyze response frame = %#v, %v; remaining=%d", response, err, stdout.Len())
	}
	if response.Status != protocol.StatusSucceeded || response.RequestID != request.RequestID || response.InputDigest != request.InputDigest || !response.Output.Valid || response.OutputDigest == "" || len(response.Diagnostics) != 0 {
		t.Fatalf("analyze response = %#v", response)
	}
}

func TestCompilerCommandAnalyzesOnlyStagedStandardExports(t *testing.T) {
	snapshot := analyzer.Snapshot{Packages: []analyzer.Package{{
		ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible,
		Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\nimport \"fmt\"\nvar Text = fmt.Sprint(1)\n")}},
	}}}
	missing := runAnalyzeSnapshot(t, snapshot)
	if missing.Status != protocol.StatusRejected || missing.Output.Valid || len(missing.Diagnostics) != 1 || missing.Diagnostics[0].Code != analyzer.CodeInvalidSnapshot {
		t.Fatalf("missing standard type input = %#v", missing)
	}
	command := exec.Command("go", "list", "-deps", "-export", "-f", "{{if .Export}}{{.ImportPath}}|{{.Export}}{{end}}", "fmt")
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(output), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		importPath, name, ok := strings.Cut(line, "|")
		if !ok {
			t.Fatalf("invalid Go export listing %q", line)
		}
		content, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		snapshot.StandardExports = append(snapshot.StandardExports, analyzer.StandardExport{ImportPath: importPath, Content: content})
	}
	selected := runAnalyzeSnapshot(t, snapshot)
	if selected.Status != protocol.StatusSucceeded || !selected.Output.Valid || len(selected.Diagnostics) != 0 {
		t.Fatalf("staged standard type input = %#v", selected)
	}
}

func TestCompilerCommandUsesTargetArchitectureAndRejectsCgo(t *testing.T) {
	snapshot := analyzer.Snapshot{Packages: []analyzer.Package{{
		ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible,
		Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\nimport \"unsafe\"\nvar _ [4]byte = [unsafe.Sizeof(uintptr(0))]byte{}\n")}},
	}}}
	on386 := runAnalyzeSnapshotWithBuild(t, snapshot, protocol.BuildContext{GOOS: "windows", GOARCH: "386"})
	if on386.Status != protocol.StatusSucceeded || !on386.Output.Valid {
		t.Fatalf("386 type check = %#v", on386)
	}
	onAMD64 := runAnalyzeSnapshot(t, snapshot)
	if onAMD64.Status != protocol.StatusRejected || onAMD64.Output.Valid || len(onAMD64.Diagnostics) != 1 || onAMD64.Diagnostics[0].Code != analyzer.CodeTypeCheck {
		t.Fatalf("amd64 type check = %#v", onAMD64)
	}
	snapshot.Packages[0].Files[0].Content = []byte("package model\n/* int value; */\nimport \"C\"\nvar Value = C.value\n")
	cgo := runAnalyzeSnapshot(t, snapshot)
	if cgo.Status != protocol.StatusRejected || cgo.Output.Valid || len(cgo.Diagnostics) == 0 || cgo.Diagnostics[0].Code != analyzer.CodeUnsupportedCapability {
		t.Fatalf("unstaged cgo type input = %#v", cgo)
	}
}

func TestCompilerCommandValidatesSelectResultsAtAnalyze(t *testing.T) {
	declarationSource := `package declaration
type NoAccess struct{}
type Ref struct { Member string; Symbol string }
type ScalarType string
type Member[Access any] struct { Namespace string; Tables []Table; Queries []Query; Access Access }
type Table struct { ID string; Columns []Column }
type Column struct { ID string; Type ScalarType }
type OperationKind string
type JoinKind string
type ColumnRef struct { Table Ref; Column string }
type JoinCondition struct { Left ColumnRef; Right ColumnRef }
type Join struct { Kind JoinKind; Table Ref; On JoinCondition }
type Query struct { ID string; Operation OperationKind; Table Ref; Joins []Join; Projection []Projection; Results []ResultField }
type Projection struct { Table Ref; Column string; Alias string }
type ResultField struct { ID string; Source string; Type ScalarType; Nullable bool }
`
	modelSource := func(results string) string {
		return `package model
import "github.com/plystra/data/declaration"
//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{
		{ID: "users", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "region-id", Type: "int64"}}},
		{ID: "regions", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "name", Type: "string"}}},
	},
	Queries: []declaration.Query{{ID: "read", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Joins: []declaration.Join{{Kind: "left", Table: declaration.Ref{Symbol: "regions"}, On: declaration.JoinCondition{
			Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "users"}, Column: "region-id"},
			Right: declaration.ColumnRef{Table: declaration.Ref{Symbol: "regions"}, Column: "id"},
		}}},
		Projection: []declaration.Projection{{Column: "id"}, {Table: declaration.Ref{Symbol: "regions"}, Column: "name", Alias: "region-name"}},
		` + results + `
	}},
}`
	}
	run := func(t *testing.T, results string) protocol.AnalyzeResponse {
		t.Helper()
		snapshot := analyzer.Snapshot{Packages: []analyzer.Package{
			{ImportPath: "github.com/plystra/data/declaration", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(declarationSource)}}},
			{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "models.go", Content: []byte(modelSource(results))}}},
		}}
		return runAnalyzeSnapshot(t, snapshot)
	}
	valid := run(t, `Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}, {ID: "name", Source: "region-name", Type: "string", Nullable: true}},`)
	if valid.Status != protocol.StatusSucceeded || !valid.Output.Valid || len(valid.Output.Roots) != 1 || len(valid.Output.Roots[0].Model.Queries[0].Results) != 2 {
		t.Fatalf("mapped SELECT analysis = %#v", valid)
	}
	for _, test := range []struct {
		name    string
		results string
		code    analyzer.DiagnosticCode
	}{
		{name: "missing results", code: analyzer.CodeInvalidRootShape},
		{name: "partial results", results: `Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}},`, code: analyzer.CodeInvalidRootShape},
		{name: "wrong scalar type", results: `Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "string"}, {ID: "name", Source: "region-name", Type: "string", Nullable: true}},`, code: analyzer.CodeTypeCheck},
		{name: "non-null left join", results: `Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}, {ID: "name", Source: "region-name", Type: "string"}},`, code: analyzer.CodeTypeCheck},
	} {
		t.Run(test.name, func(t *testing.T) {
			response := run(t, test.results)
			if response.Status != protocol.StatusRejected || response.Output.Valid || len(response.Diagnostics) != 1 || response.Diagnostics[0].Code != test.code {
				t.Fatalf("invalid SELECT result analysis = %#v", response)
			}
			if test.code == analyzer.CodeInvalidRootShape && len(response.Output.Roots) != 0 {
				t.Fatalf("invalid root shape was accepted: %#v", response.Output.Roots)
			}
		})
	}
}

func TestCompilerCommandValidatesGroupedQueryAtAnalyze(t *testing.T) {
	declarationSource := `package declaration
type NoAccess struct{}
type Ref struct { Member string; Symbol string }
type ScalarType string
type OperationKind string
type AggregateKind string
type PredicateKind string
type Member[Access any] struct { Namespace string; Tables []Table; Queries []Query; Access Access }
type Table struct { ID string; Columns []Column }
type Column struct { ID string; Type ScalarType }
type ColumnRef struct { Table Ref; Column string }
type JoinKind string
type JoinCondition struct { Left ColumnRef; Right ColumnRef }
type Join struct { Kind JoinKind; Table Ref; On JoinCondition }
type Projection struct { Table Ref; Column string; Alias string; Aggregate AggregateKind }
type Value struct { Kind ScalarType; Set bool; Parameter string; Int64 int64; String string }
type Parameter struct { ID string; Type ScalarType }
type Predicate struct { Kind PredicateKind; Left ColumnRef; Right ColumnRef; Value Value; Items []Predicate }
type ResultField struct { ID string; Source string; Type ScalarType; Nullable bool }
type OrderingDirection string
type Ordering struct { Table Ref; Column string; Direction OrderingDirection }
type Query struct { ID string; Operation OperationKind; Table Ref; Joins []Join; Parameters []Parameter; GroupBy []ColumnRef; Projection []Projection; Having Predicate; Ordering []Ordering; Results []ResultField; Limit uint64; HasLimit bool; Offset uint64; HasOffset bool }
`
	modelSource := `package model
import "github.com/plystra/data/declaration"
//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts",
	Tables: []declaration.Table{{ID: "users", Columns: []declaration.Column{{ID: "region", Type: "string"}, {ID: "id", Type: "int64"}, {ID: "team-id", Type: "int64"}}}, {ID: "teams", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "region", Type: "string"}}}},
	Queries: []declaration.Query{{ID: "count-by-region", Operation: "select", Table: declaration.Ref{Symbol: "users"},
		Joins: []declaration.Join{{Kind: "left", Table: declaration.Ref{Symbol: "teams"}, On: declaration.JoinCondition{Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "users"}, Column: "team-id"}, Right: declaration.ColumnRef{Table: declaration.Ref{Symbol: "teams"}, Column: "id"}}}},
		GroupBy: []declaration.ColumnRef{{Table: declaration.Ref{Symbol: "teams"}, Column: "region"}},
		Projection: []declaration.Projection{{Table: declaration.Ref{Symbol: "teams"}, Column: "region", Alias: "zone"}, {Aggregate: "count", Column: "id", Alias: "total"}, {Aggregate: "min", Column: "id", Alias: "minimum"}},
		Having: declaration.Predicate{Kind: "greater", Left: declaration.ColumnRef{Column: "total"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
		Ordering: []declaration.Ordering{{Column: "total", Direction: "desc"}, {Column: "zone", Direction: "asc"}},
		HasLimit: true, Limit: 2, HasOffset: true, Offset: 1,
		Results: []declaration.ResultField{{ID: "region", Source: "zone", Type: "string", Nullable: true}, {ID: "total", Source: "total", Type: "int64"}, {ID: "minimum", Source: "minimum", Type: "int64", Nullable: true}},
	}},
}`
	run := func(t *testing.T, source string) protocol.AnalyzeResponse {
		t.Helper()
		return runAnalyzeSnapshot(t, analyzer.Snapshot{Packages: []analyzer.Package{
			{ImportPath: "github.com/plystra/data/declaration", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(declarationSource)}}},
			{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "models.go", Content: []byte(source)}}},
		}})
	}
	valid := run(t, modelSource)
	if valid.Status != protocol.StatusSucceeded || !valid.Output.Valid || len(valid.Output.Roots) != 1 || len(valid.Output.Roots[0].Model.Queries[0].Projection) != 3 || len(valid.Output.Roots[0].Model.Queries[0].Ordering) != 2 || len(valid.Output.Roots[0].Model.Queries[0].Joins) != 1 || valid.Output.Roots[0].Model.Queries[0].Limit != 2 || valid.Output.Roots[0].Model.Queries[0].Offset != 1 {
		t.Fatalf("valid grouped analysis = %#v", valid)
	}
	for _, test := range []struct{ name, source string }{
		{"wrong value type", strings.Replace(modelSource, `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}`, `Value: declaration.Value{Kind: "string", Set: true, String: "1"}`, 1)},
		{"wrong output type", strings.Replace(modelSource, `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}`, `Right: declaration.ColumnRef{Column: "zone"}`, 1)},
		{"wrong parameter type", strings.Replace(strings.Replace(modelSource, `GroupBy: []declaration.ColumnRef`, `Parameters: []declaration.Parameter{{ID: "threshold", Type: "string"}}, GroupBy: []declaration.ColumnRef`, 1), `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}`, `Value: declaration.Value{Kind: "string", Set: true, Parameter: "threshold"}`, 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := run(t, test.source)
			if invalid.Status != protocol.StatusRejected || invalid.Output.Valid || len(invalid.Diagnostics) != 1 || invalid.Diagnostics[0].Code != analyzer.CodeTypeCheck {
				t.Fatalf("invalid HAVING analysis = %#v", invalid)
			}
		})
	}
	invalidNullability := run(t, strings.Replace(modelSource, `Source: "zone", Type: "string", Nullable: true`, `Source: "zone", Type: "string"`, 1))
	if invalidNullability.Status != protocol.StatusRejected || invalidNullability.Output.Valid || len(invalidNullability.Diagnostics) != 1 || invalidNullability.Diagnostics[0].Code != analyzer.CodeTypeCheck {
		t.Fatalf("invalid joined grouping result = %#v", invalidNullability)
	}
	missingGroupOrder := run(t, strings.Replace(modelSource, `{Column: "zone", Direction: "asc"}`, `{Column: "total", Direction: "asc"}`, 1))
	if missingGroupOrder.Status != protocol.StatusRejected || missingGroupOrder.Output.Valid || len(missingGroupOrder.Output.Roots) != 0 || len(missingGroupOrder.Diagnostics) != 1 || missingGroupOrder.Diagnostics[0].Code != analyzer.CodeInvalidRootShape {
		t.Fatalf("paged grouped query without key ordering = %#v", missingGroupOrder)
	}
	invalidMinimum := run(t, strings.Replace(modelSource, `Source: "minimum", Type: "int64", Nullable: true`, `Source: "minimum", Type: "int64"`, 1))
	if invalidMinimum.Status != protocol.StatusRejected || invalidMinimum.Output.Valid || len(invalidMinimum.Output.Roots) != 0 || len(invalidMinimum.Diagnostics) != 1 || invalidMinimum.Diagnostics[0].Code != analyzer.CodeInvalidRootShape {
		t.Fatalf("invalid minimum result = %#v", invalidMinimum)
	}
	for _, test := range []struct {
		name, source string
		code         analyzer.DiagnosticCode
	}{
		{"missing ordering output", strings.Replace(modelSource, `Column: "total", Direction: "desc"`, `Column: "missing", Direction: "desc"`, 1), analyzer.CodeUnresolvedReference},
		{"qualified ordering output", strings.Replace(modelSource, `Column: "total", Direction: "desc"`, `Table: declaration.Ref{Symbol: "users"}, Column: "total", Direction: "desc"`, 1), analyzer.CodeInvalidRootShape},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := run(t, test.source)
			if invalid.Status != protocol.StatusRejected || invalid.Output.Valid || len(invalid.Diagnostics) != 1 || invalid.Diagnostics[0].Code != test.code || len(invalid.Output.Roots) != 0 {
				t.Fatalf("invalid grouped ordering analysis = %#v", invalid)
			}
		})
	}
}

func runAnalyzeSnapshot(t *testing.T, snapshot analyzer.Snapshot) protocol.AnalyzeResponse {
	t.Helper()
	return runAnalyzeSnapshotWithBuild(t, snapshot, protocol.BuildContext{GOOS: "windows", GOARCH: "amd64"})
}

func runAnalyzeSnapshotWithBuild(t *testing.T, snapshot analyzer.Snapshot, build protocol.BuildContext) protocol.AnalyzeResponse {
	t.Helper()
	manifest, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), snapshot)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := protocol.AnalyzeInputDigest(manifest, build)
	if err != nil {
		t.Fatal(err)
	}
	request := protocol.AnalyzeRequest{
		Schema: protocol.AnalyzeSchema, Phase: protocol.AnalyzePhase, RequestID: "public-analyze-snapshot",
		Compiler: protocol.CompilerIdentity{ModulePath: protocol.ModulePath, ModuleVersion: "v0.0.0-test", ModuleChecksum: "h1:" + base64.StdEncoding.EncodeToString(make([]byte, 32)), ManifestDigest: "sha256:" + strings.Repeat("2", 64), CommandImportPath: protocol.CommandImportPath, AnalyzeProtocol: protocol.AnalyzeSchema, EmitProtocol: protocol.EmitSchema, DeclarationLanguage: protocol.DeclarationLanguage, GoToolchain: "go1.26"},
		Build:    build, Limits: snapshot.Limits, InputDigest: digest, Snapshot: manifest,
	}
	var input bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".")
	command.Env = append(os.Environ(), "GOWORK=off")
	command.Stdin = &input
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("compiler analyze failed: %v; stderr=%q", err, stderr.String())
	}
	var response protocol.AnalyzeResponse
	if err := protocol.ReadFrame(&stdout, &response); err != nil || stdout.Len() != 0 {
		t.Fatalf("analyze response = %#v, %v; remaining=%d", response, err, stdout.Len())
	}
	return response
}

func TestCompilerCommandRejectsImportedAggregateWrongInput(t *testing.T) {
	declarationSource := `package declaration
type NoAccess struct{}
type Ref struct { Member string; Symbol string }
type ScalarType string
type Member[Access any] struct { Namespace string; Imports []string; Tables []Table; Queries []Query; Access Access }
type Table struct { ID string; Columns []Column }
type Column struct { ID string; Type ScalarType }
type OperationKind string
type Query struct { ID string; Operation OperationKind; Table Ref; Projection []Projection; Results []ResultField }
type Projection struct { Aggregate string; Column string; Alias string }
type ResultField struct { ID string; Source string; Type ScalarType; Nullable bool }
`
	modelSource := `package model
import "github.com/plystra/data/declaration"
//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"metrics.sample/v1"},
	Queries: []declaration.Query{{ID: "minimum", Operation: "select", Table: declaration.Ref{Member: "metrics.sample/v1", Symbol: "samples"},
		Projection: []declaration.Projection{{Aggregate: "min", Column: "value", Alias: "minimum"}},
		Results: []declaration.ResultField{{ID: "minimum", Source: "minimum", Type: "int64", Nullable: true}},
	}},
}

//plystra:data metrics.sample/v1
var Samples = declaration.Member[declaration.NoAccess]{Namespace: "metrics", Tables: []declaration.Table{{ID: "samples", Columns: []declaration.Column{{ID: "value", Type: "int64"}}}}}
`
	for _, test := range []struct{ aggregate, validType, invalidType string }{
		{"min", "int64", "string"},
		{"max", "int64", "string"},
		{"sum", "float64", "int64"},
	} {
		t.Run(test.aggregate, func(t *testing.T) {
			validSource := strings.Replace(modelSource, `Aggregate: "min"`, `Aggregate: "`+test.aggregate+`"`, 1)
			validSource = strings.Replace(validSource, `Type: "int64", Nullable: true`, `Type: "`+test.validType+`", Nullable: true`, 1)
			validSource = strings.Replace(validSource, `ID: "value", Type: "int64"`, `ID: "value", Type: "`+test.validType+`"`, 1)
			snapshot := analyzer.Snapshot{Packages: []analyzer.Package{
				{ImportPath: "github.com/plystra/data/declaration", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(declarationSource)}}},
				{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "models.go", Content: []byte(validSource)}}},
			}}
			valid := runAnalyzeSnapshot(t, snapshot)
			if valid.Status != protocol.StatusSucceeded || !valid.Output.Valid || len(valid.Output.Roots) != 2 {
				t.Fatalf("valid imported aggregate analysis = %#v", valid)
			}
			snapshot.Packages[1].Files[0].Content = []byte(strings.Replace(validSource, `ID: "value", Type: "`+test.validType+`"`, `ID: "value", Type: "`+test.invalidType+`"`, 1))
			invalid := runAnalyzeSnapshot(t, snapshot)
			if invalid.Status != protocol.StatusRejected || invalid.Output.Valid || len(invalid.Diagnostics) != 1 || invalid.Diagnostics[0].Code != analyzer.CodeTypeCheck {
				t.Fatalf("invalid imported aggregate analysis = %#v", invalid)
			}
		})
	}
}

func TestCompilerCommandRejectsImportedMutationMismatch(t *testing.T) {
	declarationSource := `package declaration
type NoAccess struct{}
type Ref struct { Member string; Symbol string }
type ScalarType string
type Member[Access any] struct { Namespace string; Imports []string; Tables []Table; Queries []Query; Access Access }
type Table struct { ID string; Columns []Column }
type Column struct { ID string; Type ScalarType; Nullable bool }
type OperationKind string
type Value struct { Kind ScalarType; Set bool; String string; Int64 int64 }
type Assignment struct { Column string; Value Value; Increment bool }
type ColumnRef struct { Table Ref; Column string }
type PredicateKind string
type Predicate struct { Kind PredicateKind; Left ColumnRef; Right ColumnRef; Value Value; Items []Predicate }
type Query struct { ID string; Operation OperationKind; Table Ref; Assignments []Assignment; Predicate Predicate; Returning []Projection; Results []ResultField }
type Projection struct { Column string; Alias string }
type ResultField struct { ID string; Source string; Type ScalarType; Nullable bool }
`
	modelSource := `package model
import "github.com/plystra/data/declaration"
//plystra:data accounts.user/v1
var Accounts = declaration.Member[declaration.NoAccess]{
	Namespace: "accounts", Imports: []string{"audit.event/v1"},
	Queries: []declaration.Query{{ID: "update-event", Operation: "update", Table: declaration.Ref{Member: "audit.event/v1", Symbol: "events"},
		Assignments: []declaration.Assignment{{Column: "label", Value: declaration.Value{Kind: "string", Set: true, String: "updated"}}, {Column: "revision", Increment: true}},
		Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}},
		Returning: []declaration.Projection{{Column: "id"}, {Column: "label", Alias: "event-label"}},
		Results: []declaration.ResultField{{ID: "id", Source: "id", Type: "int64"}, {ID: "label", Source: "event-label", Type: "string", Nullable: true}},
	}},
}
//plystra:data audit.event/v1
var Events = declaration.Member[declaration.NoAccess]{Namespace: "audit", Tables: []declaration.Table{{ID: "events", Columns: []declaration.Column{{ID: "id", Type: "int64"}, {ID: "label", Type: "string", Nullable: true}, {ID: "revision", Type: "int64"}}}}}
`
	run := func(t *testing.T, source string) protocol.AnalyzeResponse {
		t.Helper()
		return runAnalyzeSnapshot(t, analyzer.Snapshot{Packages: []analyzer.Package{
			{ImportPath: "github.com/plystra/data/declaration", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "declaration.go", Content: []byte(declarationSource)}}},
			{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "models.go", Content: []byte(source)}}},
		}})
	}
	valid := run(t, modelSource)
	if valid.Status != protocol.StatusSucceeded || !valid.Output.Valid || len(valid.Output.Roots) != 2 {
		t.Fatalf("valid imported RETURNING analysis = %#v", valid)
	}
	for _, test := range []struct {
		name, source string
		code         analyzer.DiagnosticCode
	}{
		{"wrong result type", strings.Replace(modelSource, `Source: "id", Type: "int64"`, `Source: "id", Type: "string"`, 1), analyzer.CodeTypeCheck},
		{"wrong result nullability", strings.Replace(modelSource, `Source: "event-label", Type: "string", Nullable: true`, `Source: "event-label", Type: "string"`, 1), analyzer.CodeTypeCheck},
		{"wrong assignment type", strings.Replace(modelSource, `Value{Kind: "string", Set: true, String: "updated"}`, `Value{Kind: "int64", Set: true, Int64: 1}`, 1), analyzer.CodeTypeCheck},
		{"wrong increment type", strings.Replace(modelSource, `{ID: "revision", Type: "int64"}`, `{ID: "revision", Type: "string"}`, 1), analyzer.CodeTypeCheck},
		{"nullable increment", strings.Replace(modelSource, `{ID: "revision", Type: "int64"}`, `{ID: "revision", Type: "int64", Nullable: true}`, 1), analyzer.CodeTypeCheck},
		{"missing assignment column", strings.Replace(modelSource, `Assignment{{Column: "label"`, `Assignment{{Column: "missing"`, 1), analyzer.CodeUnresolvedReference},
		{"wrong predicate value type", strings.Replace(modelSource, `Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}}`, `Predicate: declaration.Predicate{Kind: "equal", Left: declaration.ColumnRef{Column: "id"}, Value: declaration.Value{Kind: "string", Set: true, String: "1"}}`, 1), analyzer.CodeTypeCheck},
		{"wrong predicate column type", strings.Replace(modelSource, `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}}`, `Right: declaration.ColumnRef{Column: "label"}}`, 1), analyzer.CodeTypeCheck},
		{"predicate missing kind", strings.Replace(modelSource, `Predicate: declaration.Predicate{Kind: "equal", `, `Predicate: declaration.Predicate{`, 1), analyzer.CodeInvalidRootShape},
		{"predicate with ignored items", strings.Replace(modelSource, `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}}`, `Value: declaration.Value{Kind: "int64", Set: true, Int64: 1}, Items: []declaration.Predicate{{Kind: "is-null", Left: declaration.ColumnRef{Column: "label"}}}}`, 1), analyzer.CodeInvalidRootShape},
	} {
		t.Run(test.name, func(t *testing.T) {
			invalid := run(t, test.source)
			if invalid.Status != protocol.StatusRejected || invalid.Output.Valid || len(invalid.Diagnostics) != 1 || invalid.Diagnostics[0].Code != test.code {
				t.Fatalf("invalid imported mutation analysis = %#v", invalid)
			}
			if test.code == analyzer.CodeInvalidRootShape {
				for _, root := range invalid.Output.Roots {
					if root.MemberID == "accounts.user/v1" {
						t.Fatalf("malformed predicate root was accepted: %#v", invalid.Output.Roots)
					}
				}
			}
		})
	}
}

func TestCompilerCommandDoesNotDependOnCorePackages(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "list", "-buildvcs=false", "-deps", ".")
	command.Env = append(os.Environ(), "GOWORK=off")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("list compiler dependencies: %v", err)
	}
	for _, path := range strings.Fields(string(output)) {
		if path == "github.com/plystra/kernel" || strings.HasPrefix(path, "github.com/plystra/kernel/") ||
			path == "github.com/plystra/cli" || strings.HasPrefix(path, "github.com/plystra/cli/") {
			t.Fatalf("compiler depends on Core package %q", path)
		}
	}
}

func TestCompilerCommandInstallsWithoutCoreModuleCache(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cache := t.TempDir()
	bin := t.TempDir()
	command := exec.CommandContext(ctx, "go", "install", "-mod=readonly", ".")
	command.Env = append(os.Environ(),
		"GOWORK=off", "GOMODCACHE="+cache, "GOPROXY=off", "GOSUMDB=off",
		"GOTOOLCHAIN=local", "GOBIN="+bin,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated compiler install: %v; output=%s", err, output)
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "plystra-data-compiler") {
		t.Fatalf("isolated compiler install output = %#v", entries)
	}
}

func TestCompilerCommandInstallsFromPublishedModuleArchive(t *testing.T) {
	const version = "v0.0.0-test"

	moduleRoot := findModuleRoot(t)
	proxy := filepath.Join(t.TempDir(), "proxy")
	writePublishedModule(t, proxy, moduleRoot, version)
	cache := filepath.Join(t.TempDir(), "module-cache")
	bin := filepath.Join(t.TempDir(), "bin")
	work := t.TempDir()
	proxyPath := filepath.ToSlash(proxy)
	if runtime.GOOS == "windows" {
		proxyPath = "/" + proxyPath
	}
	proxyURL := (&url.URL{Scheme: "file", Path: proxyPath}).String()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "install", protocol.CommandImportPath+"@"+version)
	command.Dir = work
	command.Env = append(os.Environ(),
		"GOENV=off", "GOWORK=off", "GOMODCACHE="+cache, "GOPROXY="+proxyURL,
		"GOSUMDB=off", "GONOSUMDB=*", "GONOPROXY=none", "GOTOOLCHAIN=local", "GOBIN="+bin,
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("published compiler install: %v; output=%s", err, output)
	}
	entries, err := os.ReadDir(bin)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), "plystra-data-compiler") {
		t.Fatalf("published compiler install output = %#v", entries)
	}

	installed := filepath.Join(bin, entries[0].Name())
	info, err := exec.CommandContext(ctx, "go", "version", "-m", installed).CombinedOutput()
	if err != nil {
		t.Fatalf("inspect published compiler: %v; output=%s", err, info)
	}
	if !strings.Contains(string(info), "mod\t"+protocol.ModulePath+"\t"+version) {
		t.Fatalf("installed compiler module info = %s", info)
	}
}

func findModuleRoot(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("could not locate module root")
		}
		root = parent
	}
}

func writePublishedModule(t *testing.T, proxy, moduleRoot, version string) {
	t.Helper()
	versionRoot := filepath.Join(proxy, "github.com", "plystra", "data", "@v")
	if err := os.MkdirAll(versionRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(versionRoot, "list"), version+"\n")
	writeTestFile(t, filepath.Join(versionRoot, version+".info"), fmt.Sprintf("{\"Version\":%q,\"Time\":%q}\n", version, "2026-10-08T00:00:00Z"))
	goMod, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(versionRoot, version+".mod"), string(goMod))
	archiveFile, err := os.Create(filepath.Join(versionRoot, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	archive := zip.NewWriter(archiveFile)
	prefix := "github.com/plystra/data@" + version + "/"
	var names []string
	err = filepath.WalkDir(moduleRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() {
			return nil
		}
		relative, err := filepath.Rel(moduleRoot, path)
		if err != nil {
			return err
		}
		names = append(names, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		_ = archive.Close()
		_ = archiveFile.Close()
		t.Fatal(err)
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(moduleRoot, filepath.FromSlash(name)))
		if err != nil {
			_ = archive.Close()
			_ = archiveFile.Close()
			t.Fatal(err)
		}
		header := &zip.FileHeader{Name: prefix + name, Method: zip.Deflate}
		header.SetMode(0o644)
		header.Modified = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)
		writer, err := archive.CreateHeader(header)
		if err != nil {
			_ = archive.Close()
			_ = archiveFile.Close()
			t.Fatal(err)
		}
		if _, err := writer.Write(data); err != nil {
			_ = archive.Close()
			_ = archiveFile.Close()
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		_ = archiveFile.Close()
		t.Fatal(err)
	}
	if err := archiveFile.Close(); err != nil {
		t.Fatal(err)
	}
}

func writeTestFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
