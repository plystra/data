package analyzer_test

import (
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
)

func TestAnalyzeCheckConstraintsAndMigration(t *testing.T) {
	result, err := analyzer.Analyze(sourceSnapshot(checkSource))
	if err != nil || !result.Valid || len(result.Roots) != 1 {
		t.Fatalf("CHECK source: %v, %v", err, result.Diagnostics)
	}
	model := result.Roots[0].Model
	if model.Tables[0].Constraints[0].Check.Items[1].Kind != declaration.PredicateOr || model.Migrations[0].Snapshot[0].Tables[0].Constraints[0].Check.Kind != declaration.PredicateGreater || model.Migrations[1].Operations[0].Constraint.Check.Kind != declaration.PredicateIsNotNull {
		t.Fatal("analyzer omitted live, snapshot or add-constraint CHECK intent")
	}
	before, err := declaration.DigestModel(model)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := analyzer.Analyze(sourceSnapshot(strings.Replace(checkSource, `Int64: 0`, `Int64: 1`, 1)))
	if err != nil || !changed.Valid {
		t.Fatalf("changed CHECK source: %v, %v", err, changed.Diagnostics)
	}
	after, err := declaration.DigestModel(changed.Roots[0].Model)
	if err != nil || before == after {
		t.Fatal("decoded CHECK omitted from logical digest", err)
	}
}

func TestAnalyzeCheckRejectsUnsafeSource(t *testing.T) {
	for _, test := range []struct{ name, from, to string }{
		{"parameter", `Int64: 0`, `Parameter: "amount"`},
		{"other table", `Column: "amount"`, `Table: declaration.Ref{Symbol: "elsewhere"}, Column: "amount"`},
		{"other member", `Column: "amount"`, `Table: declaration.Ref{Member: "external.data/v1", Symbol: "records"}, Column: "amount"`},
		{"wrong type", `Kind: "int64", Set: true, Int64: 0`, `Kind: "string", Set: true, String: "0"`},
		{"empty child", `Kind: "greater"`, `Kind: ""`},
		{"raw SQL", `Kind: "greater"`, `Kind: "amount > 0"`},
		{"missing migration check", `Check: declaration.Predicate{Kind: "is-not-null", Left: declaration.ColumnRef{Column: "amount"}}`, `Check: declaration.Predicate{}`},
		{"ignored operation field", `Kind: "add-constraint"`, `Kind: "create-table"`},
		{"executable expression", `Check: declaration.Predicate{Kind: "is-not-null", Left: declaration.ColumnRef{Column: "amount"}}`, `Check: buildCheck()`},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := strings.Replace(checkSource, test.from, test.to, 1)
			if body == checkSource {
				t.Fatal("invalid source probe did not change input")
			}
			body += "\nfunc buildCheck() declaration.Predicate { panic(\"must never execute\") }\n"
			result, err := analyzer.Analyze(sourceSnapshot(body))
			if err != nil || result.Valid || len(result.Diagnostics) == 0 {
				t.Fatalf("unsafe CHECK source accepted: %v, %v", err, result.Diagnostics)
			}
		})
	}
}

const checkSource = `package model
import "github.com/plystra/data/declaration"
//plystra:data checks.records/v1
var Records = declaration.Member[declaration.NoAccess]{Namespace: "checks",
 Tables: []declaration.Table{{ID: "records", Columns: []declaration.Column{{ID: "amount", Type: "int64", Nullable: true}, {ID: "state", Type: "string"}},
  Constraints: []declaration.Constraint{{ID: "valid", Kind: "check", Check: declaration.Predicate{Kind: "and", Items: []declaration.Predicate{
   {Kind: "greater", Left: declaration.ColumnRef{Column: "amount"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 0}},
   {Kind: "or", Items: []declaration.Predicate{
    {Kind: "equal", Left: declaration.ColumnRef{Column: "state"}, Value: declaration.Value{Kind: "string", Set: true, String: "pending"}},
    {Kind: "equal", Left: declaration.ColumnRef{Column: "state"}, Value: declaration.Value{Kind: "string", Set: true, String: "active"}},
   }},
  }}}},
 }},
 Migrations: []declaration.Migration{
  {ID: "initial", Version: 1, Snapshot: []declaration.MigrationSchema{{Namespace: "checks", Tables: []declaration.Table{{ID: "records", Columns: []declaration.Column{{ID: "amount", Type: "int64", Nullable: true}, {ID: "state", Type: "string"}},
   Constraints: []declaration.Constraint{{ID: "positive", Kind: "check", Check: declaration.Predicate{Kind: "greater", Left: declaration.ColumnRef{Column: "amount"}, Value: declaration.Value{Kind: "int64", Set: true, Int64: 0}}}},
  }}}}, Operations: []declaration.MigrationOperation{{Kind: "create-table", Table: declaration.Ref{Symbol: "records"}}}},
  {ID: "strict", Version: 2, Predecessors: []declaration.Ref{{Symbol: "initial"}}, Snapshot: []declaration.MigrationSchema{{Namespace: "checks", Tables: []declaration.Table{{ID: "records", Columns: []declaration.Column{{ID: "amount", Type: "int64", Nullable: true}, {ID: "state", Type: "string"}}}}}},
   Operations: []declaration.MigrationOperation{{Kind: "add-constraint", Table: declaration.Ref{Symbol: "records"}, Constraint: declaration.Constraint{ID: "required", Kind: "check", Check: declaration.Predicate{Kind: "is-not-null", Left: declaration.ColumnRef{Column: "amount"}}}}}},
 },
}
`
