package project_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/plystra/data/project"
)

func TestParseAssignmentsSortsClosedProjectDocument(t *testing.T) {
	input := []byte(`data:
  members:
    authz.persistence/v1:
      resource: database.audit
    authn.persistence/v1:
      resource: database.primary
      access: authn.persistence
`)
	got, err := project.ParseAssignments(input)
	want := []project.Assignment{
		{MemberID: "authn.persistence/v1", Resource: "database.primary", Access: "authn.persistence"},
		{MemberID: "authz.persistence/v1", Resource: "database.audit"},
	}
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("assignments = %#v, %v", got, err)
	}
	empty, err := project.ParseAssignments([]byte("data:\n  members: {}\n"))
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("empty assignments = %#v, %v", empty, err)
	}
}

func TestParseAssignmentsRejectsAmbiguousOrOpenInput(t *testing.T) {
	for _, test := range []struct {
		name, source string
	}{
		{"missing data", "members: {}\n"},
		{"unknown root", "data:\n  members: {}\nsecret: value\n"},
		{"duplicate root", "data:\n  members: {}\ndata:\n  members: {}\n"},
		{"unknown data field", "data:\n  members: {}\n  backend: postgres\n"},
		{"missing members", "data: {}\n"},
		{"scalar members", "data:\n  members: []\n"},
		{"duplicate member", "data:\n  members:\n    authn.persistence/v1: {resource: database.primary}\n    authn.persistence/v1: {resource: database.audit}\n"},
		{"invalid member", "data:\n  members:\n    authn.persistence: {resource: database.primary}\n"},
		{"unknown entry field", "data:\n  members:\n    authn.persistence/v1: {resource: database.primary, token: secret}\n"},
		{"duplicate entry field", "data:\n  members:\n    authn.persistence/v1: {resource: database.primary, resource: database.audit}\n"},
		{"missing resource", "data:\n  members:\n    authn.persistence/v1: {access: authn.persistence}\n"},
		{"null resource", "data:\n  members:\n    authn.persistence/v1: {resource: null}\n"},
		{"invalid resource", "data:\n  members:\n    authn.persistence/v1: {resource: Database.Primary}\n"},
		{"empty access", "data:\n  members:\n    authn.persistence/v1: {resource: database.primary, access: ''}\n"},
		{"duplicate access", "data:\n  members:\n    authn.persistence/v1: {resource: database.primary, access: authn.access}\n    authz.persistence/v1: {resource: database.primary, access: authn.access}\n"},
		{"database access collision", "data:\n  members:\n    authn.persistence/v1: {resource: database.primary, access: database.audit}\n    authz.persistence/v1: {resource: database.audit}\n"},
		{"alias", "data:\n  members:\n    authn.persistence/v1: &entry {resource: database.primary}\n    authz.persistence/v1: *entry\n"},
		{"second document", "data:\n  members: {}\n---\ndata:\n  members: {}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := project.ParseAssignments([]byte(test.source)); !errors.Is(err, project.ErrInvalidAssignments) {
				t.Fatalf("error = %v, want invalid assignments", err)
			}
		})
	}
}

func TestParseAssignmentsBoundsDocumentAndMemberCount(t *testing.T) {
	if _, err := project.ParseAssignments([]byte(strings.Repeat("x", project.MaxAssignmentDocumentBytes+1))); !errors.Is(err, project.ErrInvalidAssignments) {
		t.Fatalf("oversized document error = %v", err)
	}
	var input strings.Builder
	input.WriteString("data:\n  members:\n")
	for index := 0; index <= project.MaxMembers; index++ {
		input.WriteString("    member.item" + strings.Repeat("a", index+1) + "/v1: {resource: database.primary}\n")
	}
	if _, err := project.ParseAssignments([]byte(input.String())); !errors.Is(err, project.ErrInvalidAssignments) {
		t.Fatalf("too many members error = %v", err)
	}
}
