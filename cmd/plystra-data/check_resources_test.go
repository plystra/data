package main

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/plystra/data/project"
)

const coreResourceFixture = `{"schema":"plystra.result/v1","operation":"inspect.resources","invocation_id":"abc","snapshot":{"selector":{"mode":"default"}},"status":"success","exit_class":0,"changes":[],"diagnostics":[],"recovery":[],"effects":{"observed":[],"planned":[],"skipped":[],"unverified":[]},"support":[],"payload":{"schema":"plystra.resource-selections/v1","module_path":"example.com/app","scope":"configured_only","instances":[{"name":"access.generated","provider":"example.com/app/dataaccess.New","source_path":"plystra.yaml","source_line":3,"source_column":7,"provider_visible":false},{"name":"database.primary","provider":"github.com/plystra/data/postgres.New","source_path":"plystra.yaml","source_line":5,"source_column":7,"provider_visible":true,"resource_id":"data.database/v1"}]},"continuation":null}`

func TestParseCoreResourceSelectionsAcceptsConfiguredOnlyResult(t *testing.T) {
	got, err := parseCoreResourceSelections([]byte(coreResourceFixture + "\n"))
	want := project.ConfiguredResources{ModulePath: "example.com/app", Instances: []project.ConfiguredResource{
		{Name: "access.generated", Provider: "example.com/app/dataaccess.New", SourcePath: "plystra.yaml", SourceLine: 3, SourceColumn: 7},
		{Name: "database.primary", Provider: "github.com/plystra/data/postgres.New", SourcePath: "plystra.yaml", SourceLine: 5, SourceColumn: 7, ProviderVisible: true, ResourceID: "data.database/v1"},
	}}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Core selection = %#v, %v; want %#v", got, err, want)
	}
}

func TestCoreSelectionEnvironmentUsesDefaultAndOfflineContext(t *testing.T) {
	t.Setenv("PLYSTRA_ENV", "production")
	t.Setenv("PLYSTRA_CONFIG", "deploy/other.yaml")
	t.Setenv("GOPROXY", "https://proxy.example")
	t.Setenv("GOSUMDB", "sumdb.example")
	for _, test := range []struct {
		offline bool
		proxy   string
	}{
		{proxy: "https://proxy.example"},
		{offline: true, proxy: "off"},
	} {
		values := make(map[string]string)
		for _, entry := range coreSelectionEnvironment(test.offline) {
			key, value, _ := strings.Cut(entry, "=")
			values[strings.ToUpper(key)] = value
		}
		if _, exists := values["PLYSTRA_ENV"]; exists {
			t.Fatal("Core environment inherited PLYSTRA_ENV")
		}
		if _, exists := values["PLYSTRA_CONFIG"]; exists {
			t.Fatal("Core environment inherited PLYSTRA_CONFIG")
		}
		if values["GOPROXY"] != test.proxy || values["GOSUMDB"] != map[bool]string{true: "off", false: "sumdb.example"}[test.offline] || values["PATH"] != os.Getenv("PATH") {
			t.Fatalf("Core environment = %#v", values)
		}
	}
}

func TestParseCoreResourceSelectionsRejectsInvalidResult(t *testing.T) {
	for _, test := range []struct{ name, old, replacement string }{
		{"schema", `"plystra.result/v1"`, `"plystra.result/v2"`},
		{"operation", `"inspect.resources"`, `"inspect.interfaces"`},
		{"status", `"status":"success"`, `"status":"partial"`},
		{"exit", `"exit_class":0`, `"exit_class":3`},
		{"selector", `"mode":"default"`, `"mode":"environment"`},
		{"payload schema", `"plystra.resource-selections/v1"`, `"plystra.graph/v1"`},
		{"scope", `"configured_only"`, `"resolved"`},
		{"effects", `"observed":[]`, `"observed":[{}]`},
		{"missing inventory", `"instances":[`, `"other":[`},
		{"unknown field", `"scope":"configured_only"`, `"scope":"configured_only","private":"value"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			changed := strings.Replace(coreResourceFixture, test.old, test.replacement, 1)
			if changed == coreResourceFixture {
				t.Fatal("fixture replacement missed")
			}
			if _, err := parseCoreResourceSelections([]byte(changed)); err == nil {
				t.Fatal("invalid Core result accepted")
			}
		})
	}
	if _, err := parseCoreResourceSelections([]byte(coreResourceFixture + "{}")); err == nil {
		t.Fatal("trailing JSON accepted")
	}
}
