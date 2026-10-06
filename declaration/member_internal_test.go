package declaration

import "testing"

func TestNodeCountIncludesConflict(t *testing.T) {
	model := Model{Queries: []Query{{Conflict: Conflict{Constraint: "users-pk"}}}}
	if got, want := nodeCount(model), 3; got != want {
		t.Fatalf("nodeCount = %d, want %d", got, want)
	}
}

func TestNodeCountIncludesConflictUpdateColumns(t *testing.T) {
	model := Model{Queries: []Query{{Conflict: Conflict{Constraint: "users-pk", Action: ConflictDoUpdate, UpdateColumns: []string{"email", "name"}}}}}
	if got, want := nodeCount(model), 5; got != want {
		t.Fatalf("nodeCount = %d, want %d", got, want)
	}
}
