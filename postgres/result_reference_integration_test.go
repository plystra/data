//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/plystra/data/database"
	"github.com/plystra/kernel/invocation"
)

func linkedPlan(label string) database.Plan {
	kinds := []database.ValueKind{database.ValueInt64, database.ValueString, database.ValueBool, database.ValueFloat64, database.ValueBytes}
	columns := make([]database.ResultColumn, len(kinds))
	bindings := make([]database.Argument, len(kinds))
	for index, kind := range kinds {
		columns[index].Kind = kind
		bindings[index] = database.ResultArgument(kind, "create", uint32(index))
	}
	return database.Plan{Statements: []database.Statement{
		{ID: "create", SQL: "INSERT INTO parents (label,active,score,token) VALUES ($1,$2,$3,$4) RETURNING id,label,active,score,token",
			Args:        []database.Argument{database.StringArgument(label), database.BoolArgument(true), database.Float64Argument(1.5), database.BytesArgument([]byte("bounded-value"))},
			ReturnsRows: true, Cardinality: database.CardinalityExactlyOne, Affected: database.ExactAffectedRows(1), ResultColumns: columns},
		{ID: "dependent", SQL: "INSERT INTO children (id,label,active,score,token) VALUES ($1,$2,$3,$4,$5)", Args: bindings, Affected: database.ExactAffectedRows(1)},
	}}
}

func TestPostgresEarlierResultBindingsCommitAndRollback(t *testing.T) {
	ctx, provider, conn := migrationDatabase(t)
	if _, err := conn.Exec(ctx, "CREATE TABLE parents (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, label text NOT NULL, active bool NOT NULL, score double precision NOT NULL, token bytea NOT NULL); CREATE TABLE children (id bigint PRIMARY KEY REFERENCES parents(id), label text NOT NULL, active bool NOT NULL, score double precision NOT NULL, token bytea NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	result, err := provider.Exec(ctx, linkedPlan("committed"))
	if err != nil || len(result.Statements) != 2 || result.Statements[0].AffectedRows != 1 || result.Statements[1].AffectedRows != 1 {
		t.Fatalf("linked atomic result = %#v, %v", result, err)
	}
	id := result.Statements[0].Rows[0][0].Int64
	var label string
	var active bool
	var score float64
	var token []byte
	if err := conn.QueryRow(ctx, "SELECT label,active,score,token FROM children WHERE id=$1", id).Scan(&label, &active, &score, &token); err != nil || label != "committed" || !active || score != 1.5 || string(token) != "bounded-value" {
		t.Fatal("earlier typed result did not reach dependent mutation")
	}
	for _, test := range []struct {
		name string
		edit func(*database.Plan)
		want error
	}{
		{"returning affected mismatch", func(p *database.Plan) { p.Statements[0].Affected = database.ExactAffectedRows(2) }, database.ErrBackend},
		{"dependent affected mismatch", func(p *database.Plan) { p.Statements[1].Affected = database.ExactAffectedRows(2) }, database.ErrBackend},
		{"dependent constraint failure", func(p *database.Plan) {
			p.Statements[1].SQL = "INSERT INTO children (id,label,active,score,token) VALUES ($1::bigint-1000000,$2,$3,$4,$5)"
		}, database.ErrForeignKey},
		{"typed output failure", func(p *database.Plan) {
			p.Statements[0].SQL = "INSERT INTO parents (label,active,score,token) VALUES ($1,$2,$3,$4) RETURNING id::text,label,active,score,token"
		}, database.ErrBackend},
		{"no earlier row", func(p *database.Plan) {
			p.Statements[0].SQL = "INSERT INTO parents (label,active,score,token) SELECT $1,$2,$3,$4 WHERE false RETURNING id,label,active,score,token"
		}, database.ErrNotFound},
		{"extra earlier rows", func(p *database.Plan) {
			p.Statements[0].SQL = "INSERT INTO parents (label,active,score,token) SELECT $1,$2,$3,$4 FROM generate_series(1,2) RETURNING id,label,active,score,token"
		}, database.ErrBackend},
	} {
		t.Run(test.name, func(t *testing.T) {
			plan := linkedPlan(test.name)
			test.edit(&plan)
			result, err := provider.Exec(ctx, plan)
			if !errors.Is(err, test.want) || len(result.Statements) != 0 || invocation.NewSemanticError("storage_failed", err).Completion() != invocation.CompletionResultKnown {
				t.Fatalf("failed linked plan = %#v, %v", result, err)
			}
			var count int
			if err := conn.QueryRow(ctx, "SELECT (SELECT count(*) FROM parents WHERE label=$1) + (SELECT count(*) FROM children WHERE label=$1)", test.name).Scan(&count); err != nil || count != 0 {
				t.Fatalf("failed plan published database effects: count=%d err=%v", count, err)
			}
		})
	}
	plan := linkedPlan("cancelled")
	plan.Statements[1] = database.Statement{ID: "wait", SQL: "SELECT $1::bigint FROM pg_sleep(2)", Args: []database.Argument{database.ResultArgument(database.ValueInt64, "create", 0)}, ReturnsRows: true, Cardinality: database.CardinalityExactlyOne, ResultColumns: []database.ResultColumn{{Kind: database.ValueInt64}}}
	cancelled, cancel := context.WithTimeout(ctx, 75*time.Millisecond)
	defer cancel()
	if result, err := provider.Exec(cancelled, plan); len(result.Statements) != 0 || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancelled plan published a result: %#v, %v", result, err)
	}
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM parents WHERE label='cancelled'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("cancelled plan left effects: count=%d err=%v", count, err)
	}
}
