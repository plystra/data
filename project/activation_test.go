package project_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/project"
)

func TestValidateActivationSelectsAndSortsAssignedRoots(t *testing.T) {
	authn := analyzer.Root{
		MemberID:      "authn.persistence/v1",
		Model:         declaration.Model{Namespace: "authn", Imports: []string{"audit.events/v1"}},
		AccessPackage: "example.com/authn/access",
		AccessType:    "Resource",
		AccessID:      "authn.access/v1",
	}
	audit := analyzer.Root{
		MemberID:      "audit.events/v1",
		Model:         declaration.Model{Namespace: "audit"},
		MigrationOnly: true,
	}
	dormant := analyzer.Root{
		MemberID:      "billing.ledger/v1",
		Model:         declaration.Model{Namespace: "authn"},
		MigrationOnly: true,
	}
	assignments := []project.Assignment{
		{MemberID: authn.MemberID, Resource: "database.primary", Access: "authn.persistence"},
		{MemberID: audit.MemberID, Resource: "database.primary"},
	}
	authnDigest, err := declaration.DigestModel(authn.Model)
	if err != nil {
		t.Fatal(err)
	}
	auditDigest, err := declaration.DigestModel(audit.Model)
	if err != nil {
		t.Fatal(err)
	}
	if authnDigest == auditDigest {
		t.Fatal("different member models have the same digest")
	}
	want := []project.ActiveMember{
		{MemberID: audit.MemberID, Resource: "database.primary", Namespace: "audit", MigrationOnly: true, ModelDigest: auditDigest},
		{MemberID: authn.MemberID, Resource: "database.primary", Namespace: "authn", Access: "authn.persistence",
			AccessPackage: authn.AccessPackage, AccessType: authn.AccessType, AccessID: authn.AccessID, ModelDigest: authnDigest},
	}
	for _, test := range []struct {
		assignments []project.Assignment
		roots       []analyzer.Root
	}{
		{assignments, []analyzer.Root{dormant, authn, audit}},
		{[]project.Assignment{assignments[1], assignments[0]}, []analyzer.Root{audit, authn, dormant}},
	} {
		got, err := project.ValidateActivation(test.assignments, test.roots)
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("activation = %#v, %v; want %#v", got, err, want)
		}
	}
	got, err := project.ValidateActivation(nil, []analyzer.Root{authn, audit, dormant})
	if err != nil || len(got) != 0 {
		t.Fatalf("unassigned roots activated: %#v, %v", got, err)
	}
}

func TestValidateActivationAllowsNamespaceReuseOnDifferentResources(t *testing.T) {
	roots := []analyzer.Root{
		{MemberID: "audit.events/v1", Model: declaration.Model{Namespace: "shared"}, MigrationOnly: true},
		{MemberID: "billing.ledger/v1", Model: declaration.Model{Namespace: "shared"}, MigrationOnly: true},
	}
	assignments := []project.Assignment{
		{MemberID: roots[0].MemberID, Resource: "database.primary"},
		{MemberID: roots[1].MemberID, Resource: "database.audit"},
	}
	got, err := project.ValidateActivation(assignments, roots)
	if err != nil || len(got) != 2 {
		t.Fatalf("separate Resource namespaces = %#v, %v", got, err)
	}
}

func TestValidateActivationRejectsInvalidAssignments(t *testing.T) {
	authn := analyzer.Root{
		MemberID:      "authn.persistence/v1",
		Model:         declaration.Model{Namespace: "authn"},
		AccessPackage: "example.com/authn/access",
		AccessType:    "Resource",
		AccessID:      "authn.access/v1",
	}
	audit := analyzer.Root{
		MemberID:      "audit.events/v1",
		Model:         declaration.Model{Namespace: "audit"},
		MigrationOnly: true,
	}
	authnAssignment := project.Assignment{MemberID: authn.MemberID, Resource: "database.primary", Access: "authn.persistence"}
	auditAssignment := project.Assignment{MemberID: audit.MemberID, Resource: "database.primary"}
	for _, test := range []struct {
		name        string
		assignments []project.Assignment
		roots       []analyzer.Root
		message     string
	}{
		{"missing assigned root", []project.Assignment{authnAssignment}, []analyzer.Root{audit}, "no accepted analyze root"},
		{"access required", []project.Assignment{{MemberID: authn.MemberID, Resource: "database.primary"}}, []analyzer.Root{authn}, "requires an accepted access"},
		{"access contract required", []project.Assignment{authnAssignment}, []analyzer.Root{{MemberID: authn.MemberID, Model: authn.Model}}, "requires an accepted access"},
		{"access forbidden", []project.Assignment{{MemberID: audit.MemberID, Resource: "database.primary", Access: "audit.access"}}, []analyzer.Root{audit}, "cannot select access"},
		{"migration-only query", []project.Assignment{auditAssignment}, []analyzer.Root{{MemberID: audit.MemberID, Model: declaration.Model{Namespace: "audit", Queries: []declaration.Query{{ID: "read"}}}, MigrationOnly: true}}, "cannot declare queries or plans"},
		{"migration-only plan", []project.Assignment{auditAssignment}, []analyzer.Root{{MemberID: audit.MemberID, Model: declaration.Model{Namespace: "audit", Plans: []declaration.Plan{{ID: "read"}}}, MigrationOnly: true}}, "cannot declare queries or plans"},
		{"unassigned import", []project.Assignment{authnAssignment}, []analyzer.Root{withImport(authn, audit.MemberID), audit}, "imports unassigned member"},
		{"cross Resource import", []project.Assignment{authnAssignment, {MemberID: audit.MemberID, Resource: "database.audit"}}, []analyzer.Root{withImport(authn, audit.MemberID), audit}, "across Resource instances"},
		{"same Resource namespace", []project.Assignment{authnAssignment, auditAssignment}, []analyzer.Root{authn, {MemberID: audit.MemberID, Model: declaration.Model{Namespace: "authn"}, MigrationOnly: true}}, "claim namespace"},
		{"access collides with database Resource", []project.Assignment{{MemberID: authn.MemberID, Resource: "database.primary", Access: "database.audit"}, {MemberID: audit.MemberID, Resource: "database.audit"}}, []analyzer.Root{authn, audit}, "collides with a Resource"},
		{"duplicate access", []project.Assignment{authnAssignment, {MemberID: "billing.ledger/v1", Resource: "database.audit", Access: authnAssignment.Access}}, []analyzer.Root{authn, {MemberID: "billing.ledger/v1", Model: declaration.Model{Namespace: "billing"}, AccessPackage: authn.AccessPackage, AccessType: authn.AccessType, AccessID: "billing.access/v1"}}, "belongs to both"},
		{"reserved access name", []project.Assignment{{MemberID: authn.MemberID, Resource: "database.primary", Access: "kernel.audit"}}, []analyzer.Root{authn}, "reserved name"},
		{"duplicate assignment ID", []project.Assignment{authnAssignment, {MemberID: authn.MemberID, Resource: "database.audit", Access: "authn.other"}}, []analyzer.Root{authn}, "duplicate assignment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := project.ValidateActivation(test.assignments, test.roots)
			if !errors.Is(err, project.ErrInvalidActivation) || !strings.Contains(err.Error(), test.message) || got != nil {
				t.Fatalf("activation = %#v, %v; want invalid activation containing %q", got, err, test.message)
			}
		})
	}
}

func withImport(root analyzer.Root, memberID string) analyzer.Root {
	root.Model.Imports = []string{memberID}
	return root
}
