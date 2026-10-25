//go:build integration

package postgres_test

import (
	"errors"
	"testing"

	"github.com/plystra/data/database"
)

func TestPostgresBoundInputsComposeWithEarlierResults(t *testing.T) {
	ctx, provider, conn := migrationDatabase(t)
	if _, err := conn.Exec(ctx, "CREATE TABLE parents (id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY, label text NOT NULL, active bool NOT NULL, score double precision NOT NULL, token bytea NOT NULL); CREATE TABLE children (id bigint PRIMARY KEY REFERENCES parents(id), label text NOT NULL, active bool NOT NULL, score double precision NOT NULL, token bytea NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	plan := linkedPlan("")
	label := "bound '); DROP TABLE parents; --"
	plan.Inputs = map[string]database.Argument{
		"label": database.StringArgument(label), "active": database.BoolArgument(true),
		"score": database.Float64Argument(1.5), "token": database.BytesArgument(nil),
	}
	plan.Statements[0].Args = []database.Argument{
		database.InputArgument(database.ValueString, "label"), database.InputArgument(database.ValueBool, "active"),
		database.InputArgument(database.ValueFloat64, "score"), database.InputArgument(database.ValueBytes, "token"),
	}
	result, err := provider.Exec(ctx, plan)
	if err != nil || len(result.Statements) != 2 {
		t.Fatal("bound input and dependent result did not commit", err)
	}
	id := result.Statements[0].Rows[0][0].Int64
	var matches bool
	if err := conn.QueryRow(ctx, "SELECT label=$2 AND active AND score=1.5 AND token=''::bytea FROM children WHERE id=$1", id, label).Scan(&matches); err != nil || !matches {
		t.Fatal("bound values changed or were interpolated", err)
	}
	plan.Inputs["label"] = database.StringArgument("must-roll-back")
	plan.Inputs["id"] = database.Int64Argument(id)
	plan.Statements[1].Args[0] = database.InputArgument(database.ValueInt64, "id")
	result, err = provider.Exec(ctx, plan)
	if !errors.Is(err, database.ErrConflict) || len(result.Statements) != 0 {
		t.Fatal("later bound constraint failure exposed results", err)
	}
	var count int
	if err := conn.QueryRow(ctx, "SELECT count(*) FROM parents WHERE label='must-roll-back'").Scan(&count); err != nil || count != 0 {
		t.Fatal("bound input plan did not roll back atomically", err)
	}
}
