package postgres_test

import (
	"strings"
	"testing"

	postgres "github.com/plystra/data/postgres/compiler"
)

func TestDigestSQLIsStableAndContentBound(t *testing.T) {
	first := postgres.DigestSQL("CREATE TABLE tasks (id bigint PRIMARY KEY)")
	if !strings.HasPrefix(first, "sha256:") || len(first) != len("sha256:")+64 {
		t.Fatalf("digest = %q", first)
	}
	if first != postgres.DigestSQL("CREATE TABLE tasks (id bigint PRIMARY KEY)") {
		t.Fatal("same SQL changed its digest")
	}
	if first == postgres.DigestSQL("CREATE TABLE tasks (id bigint)") {
		t.Fatal("different SQL reused its digest")
	}
}
