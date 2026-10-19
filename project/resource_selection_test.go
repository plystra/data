package project_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/plystra/data/project"
)

func TestValidateDatabaseSelectionsAllowsPendingAccessProvider(t *testing.T) {
	resources := project.ConfiguredResources{ModulePath: "example.com/app", Instances: []project.ConfiguredResource{
		{Name: "access.generated", Provider: "example.com/app/dataaccess.New", SourcePath: "plystra.yaml", SourceLine: 3, SourceColumn: 7},
		{Name: "database.audit", Provider: "github.com/plystra/data/postgres.New", SourcePath: "plystra.yaml", SourceLine: 5, SourceColumn: 7, ProviderVisible: true, ResourceID: "data.database/v1"},
		{Name: "database.primary", Provider: "github.com/plystra/data/postgres.New", SourcePath: "plystra.yaml", SourceLine: 7, SourceColumn: 7, ProviderVisible: true, ResourceID: "data.database/v1"},
	}}
	active := []project.ActiveMember{
		{MemberID: "authn.persistence/v1", Resource: "database.primary", Access: "access.generated"},
		{MemberID: "audit.log/v1", Resource: "database.audit", MigrationOnly: true},
		{MemberID: "authz.persistence/v1", Resource: "database.primary", MigrationOnly: true},
	}
	got, err := project.ValidateDatabaseSelections(active, "example.com/app", resources)
	want := []project.DatabaseSelection{
		{Resource: "database.audit", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1"},
		{Resource: "database.primary", Provider: "github.com/plystra/data/postgres.New", Backend: "postgres/v1"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("database selection = %#v, %v; want %#v", got, err, want)
	}
}

func TestValidateDatabaseSelectionsRejectsMissingOrUnsupportedDatabase(t *testing.T) {
	good := project.ConfiguredResource{Name: "database.primary", Provider: "github.com/plystra/data/postgres.New", SourcePath: "plystra.yaml", SourceLine: 4, SourceColumn: 7, ProviderVisible: true, ResourceID: "data.database/v1"}
	member := project.ActiveMember{MemberID: "audit.log/v1", Resource: good.Name, MigrationOnly: true}
	for _, test := range []struct {
		name      string
		module    string
		instances []project.ConfiguredResource
		message   string
	}{
		{"missing", "example.com/app", nil, "missing database instance"},
		{"invisible", "example.com/app", []project.ConfiguredResource{{Name: good.Name, Provider: good.Provider, SourcePath: good.SourcePath, SourceLine: good.SourceLine, SourceColumn: good.SourceColumn}}, "not visible"},
		{"wrong contract", "example.com/app", []project.ConfiguredResource{{Name: good.Name, Provider: good.Provider, SourcePath: good.SourcePath, SourceLine: good.SourceLine, SourceColumn: good.SourceColumn, ProviderVisible: true, ResourceID: "other.database/v1"}}, "requires the selected PostgreSQL"},
		{"wrong provider", "example.com/app", []project.ConfiguredResource{{Name: good.Name, Provider: "example.com/app/other.New", SourcePath: good.SourcePath, SourceLine: good.SourceLine, SourceColumn: good.SourceColumn, ProviderVisible: true, ResourceID: good.ResourceID}}, "requires the selected PostgreSQL"},
		{"wrong Project", "example.com/other", []project.ConfiguredResource{good}, "Project identity"},
		{"duplicate", "example.com/app", []project.ConfiguredResource{good, good}, "malformed configured Resource fact"},
		{"unsafe source", "example.com/app", []project.ConfiguredResource{{Name: good.Name, Provider: good.Provider, SourcePath: "../private", SourceLine: good.SourceLine, SourceColumn: good.SourceColumn, ProviderVisible: true, ResourceID: good.ResourceID}}, "malformed configured Resource fact"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := project.ValidateDatabaseSelections([]project.ActiveMember{member}, "example.com/app", project.ConfiguredResources{ModulePath: test.module, Instances: test.instances})
			if !errors.Is(err, project.ErrDatabaseSelection) || !strings.Contains(err.Error(), test.message) || got != nil {
				t.Fatalf("database selection = %#v, %v; want %q", got, err, test.message)
			}
		})
	}
}
