package declaration_test

import (
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/plystra/data/declaration"
)

func TestCheckNormalizeOwnsExpressionsAndBindsDigest(t *testing.T) {
	member := checkMember()
	model, err := member.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	before, err := member.Digest()
	if err != nil {
		t.Fatal(err)
	}
	model.Tables[0].Constraints[0].Check.Items[0].Value.Int64 = 7
	model.Migrations[0].Snapshot[0].Tables[0].Constraints[0].Check.Items[0].Value.Int64 = 8
	model.Migrations[1].Operations[0].Constraint.Check.Items[0].Value.Int64 = 9
	if member.Tables[0].Constraints[0].Check.Items[0].Value.Int64 != 0 || member.Migrations[0].Snapshot[0].Tables[0].Constraints[0].Check.Items[0].Value.Int64 != 0 || member.Migrations[1].Operations[0].Constraint.Check.Items[0].Value.Int64 != 0 {
		t.Fatal("normalized CHECK aliases authored expressions")
	}
	for _, change := range []func(*declaration.Member[declaration.NoAccess]){
		func(m *declaration.Member[declaration.NoAccess]) {
			m.Tables[0].Constraints[0].Check.Items[0].Value.Int64 = 2
		},
		func(m *declaration.Member[declaration.NoAccess]) {
			m.Migrations[0].Snapshot[0].Tables[0].Constraints[0].Check.Items[0].Value.Int64 = 2
		},
		func(m *declaration.Member[declaration.NoAccess]) {
			m.Migrations[1].Operations[0].Constraint.Check.Items[0].Value.Int64 = 2
		},
	} {
		changed := checkMember()
		change(&changed)
		digest, err := changed.Digest()
		if err != nil || digest == before {
			t.Fatalf("CHECK intent not bound to digest: %v", err)
		}
	}
	encoded, err := json.Marshal(member)
	if err != nil {
		t.Fatal(err)
	}
	var decoded declaration.Member[declaration.NoAccess]
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if digest, err := decoded.Digest(); err != nil || digest != before {
		t.Fatal("CHECK JSON roundtrip changed intent", err)
	}
}

func TestCheckRejectsUnsafeOrIgnoredExpressions(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*declaration.Constraint)
	}{
		{"empty", func(c *declaration.Constraint) { c.Check = declaration.Predicate{} }},
		{"empty child", func(c *declaration.Constraint) { c.Check.Items[0] = declaration.Predicate{} }},
		{"one boolean child", func(c *declaration.Constraint) { c.Check.Items = c.Check.Items[:1] }},
		{"raw SQL kind", func(c *declaration.Constraint) { c.Check.Kind = "amount > 0" }},
		{"parameter", func(c *declaration.Constraint) { c.Check.Items[0].Value.Parameter = "untrusted" }},
		{"other table", func(c *declaration.Constraint) { c.Check.Items[0].Left.Table.Symbol = "other" }},
		{"other member", func(c *declaration.Constraint) {
			c.Check.Items[0].Left.Table = declaration.Ref{Member: "other.data/v1", Symbol: "records"}
		}},
		{"missing column", func(c *declaration.Constraint) { c.Check.Items[0].Left.Column = "missing" }},
		{"wrong literal type", func(c *declaration.Constraint) { c.Check.Items[0].Value = declaration.String("0") }},
		{"wrong column type", func(c *declaration.Constraint) {
			c.Check.Items[0].Value = declaration.Value{}
			c.Check.Items[0].Right.Column = "state"
		}},
		{"mixed operands", func(c *declaration.Constraint) { c.Check.Items[0].Right.Column = "amount" }},
		{"ignored literal payload", func(c *declaration.Constraint) { c.Check.Items[0].Value.String = "ignored" }},
		{"comparison children", func(c *declaration.Constraint) { c.Check.Items[0].Items = []declaration.Predicate{} }},
		{"boolean operands", func(c *declaration.Constraint) { c.Check.Left.Column = "amount" }},
		{"constraint columns", func(c *declaration.Constraint) { c.Columns = []string{"amount"} }},
		{"constraint foreign fields", func(c *declaration.Constraint) { c.ReferencedTable.Symbol = "records" }},
		{"CHECK on unique", func(c *declaration.Constraint) { c.Kind = declaration.ConstraintUnique; c.Columns = []string{"amount"} }},
		{"null with value", func(c *declaration.Constraint) { c.Check.Items[0].Kind = declaration.PredicateIsNull }},
	} {
		t.Run(test.name, func(t *testing.T) {
			member := checkMember()
			test.change(&member.Tables[0].Constraints[0])
			if _, err := member.Normalize(); err == nil {
				t.Fatal("unsafe CHECK accepted")
			}
		})
	}
}

func TestCheckComparisonAndNullVocabulary(t *testing.T) {
	for _, kind := range []declaration.PredicateKind{declaration.PredicateEqual, declaration.PredicateNotEqual, declaration.PredicateLess, declaration.PredicateLessEqual, declaration.PredicateGreater, declaration.PredicateGreaterEqual} {
		for _, column := range []bool{false, true} {
			member := checkMember()
			predicate := declaration.Predicate{Kind: kind, Left: declaration.ColumnRef{Table: declaration.Ref{Symbol: "records"}, Column: "amount"}, Value: declaration.Int64(1)}
			if column {
				predicate.Right.Column = "limit"
				predicate.Value = declaration.Value{}
			}
			member.Tables[0].Constraints[0].Check = predicate
			if _, err := member.Normalize(); err != nil {
				t.Fatal("same-table comparison rejected", err)
			}
		}
	}
	for _, kind := range []declaration.PredicateKind{declaration.PredicateIsNull, declaration.PredicateIsNotNull} {
		member := checkMember()
		member.Tables[0].Constraints[0].Check = declaration.Predicate{Kind: kind, Left: declaration.ColumnRef{Column: "amount"}}
		if _, err := member.Normalize(); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct {
		kind    declaration.ScalarType
		value   declaration.Value
		invalid bool
	}{
		{declaration.TypeBool, declaration.Bool(true), false},
		{declaration.TypeBytes, declaration.Bytes("00ff"), false},
		{declaration.TypeFloat64, declaration.Float64(1.5), false},
		{declaration.TypeUint64, declaration.Uint64(1 << 63), false},
		{declaration.TypeBytes, declaration.Bytes("zz"), true},
		{declaration.TypeFloat64, declaration.Float64(math.NaN()), true},
		{declaration.TypeFloat64, declaration.Float64(math.Inf(1)), true},
		{declaration.TypeString, declaration.String("a\x00b"), true},
	} {
		member := checkMember()
		member.Tables[0].Columns[0].Type = test.kind
		member.Tables[0].Constraints[0].Check = declaration.Predicate{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "amount"}, Value: test.value}
		_, err := member.Normalize()
		if (err != nil) != test.invalid {
			t.Fatalf("closed scalar %s acceptance mismatch: %v", test.kind, err)
		}
	}
}

func TestCheckBoundsBeforeCopy(t *testing.T) {
	cycle := []declaration.Predicate{{Kind: declaration.PredicateAnd}}
	cycle[0].Items = cycle
	for _, location := range []string{"table", "snapshot", "operation"} {
		member := checkMember()
		switch location {
		case "table":
			member.Tables[0].Constraints[0].Check = cycle[0]
		case "snapshot":
			member.Migrations[0].Snapshot[0].Tables[0].Constraints[0].Check = cycle[0]
		case "operation":
			member.Migrations[1].Operations[0].Constraint.Check = cycle[0]
		}
		if _, err := member.Normalize(); !errors.Is(err, declaration.ErrLimit) {
			t.Fatalf("cyclic %s did not fail boundedly: %v", location, err)
		}
	}
	member := checkMember()
	member.Tables[0].Constraints[0].Check.Items = make([]declaration.Predicate, declaration.MaximumNodes)
	if _, err := member.Normalize(); !errors.Is(err, declaration.ErrLimit) {
		t.Fatal("wide expression bypassed node budget", err)
	}
}

func TestCheckAddConstraintMigrationRejectsIgnoredIntent(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*declaration.Migration)
	}{
		{"already present", func(m *declaration.Migration) { m.Operations[0].Constraint.ID = "positive" }},
		{"duplicate operation", func(m *declaration.Migration) { m.Operations = append(m.Operations, m.Operations[0]) }},
		{"unsupported unique", func(m *declaration.Migration) { m.Operations[0].Constraint.Kind = declaration.ConstraintUnique }},
		{"ignored column", func(m *declaration.Migration) { m.Operations[0].Column.ID = "ignored" }},
		{"ignored index", func(m *declaration.Migration) { m.Operations[0].Index.ID = "ignored" }},
		{"missing expression", func(m *declaration.Migration) { m.Operations[0].Constraint.Check = declaration.Predicate{} }},
		{"ignored create constraint", func(m *declaration.Migration) { m.Operations[0].Kind = declaration.MigrationCreateTable }},
		{"ignored add-column constraint", func(m *declaration.Migration) { m.Operations[0].Kind = declaration.MigrationAddColumn }},
		{"ignored create-index constraint", func(m *declaration.Migration) { m.Operations[0].Kind = declaration.MigrationCreateIndex }},
	} {
		t.Run(test.name, func(t *testing.T) {
			member := checkMember()
			test.change(&member.Migrations[1])
			if _, err := member.Normalize(); err == nil {
				t.Fatal("invalid add-constraint accepted")
			}
		})
	}
}

func TestCheckAbsentFieldsPreserveHistoricalJSON(t *testing.T) {
	constraint := declaration.Constraint{ID: "pk", Kind: declaration.ConstraintPrimaryKey, Columns: []string{"amount"}}
	encoded, err := json.Marshal(constraint)
	if err != nil {
		t.Fatal(err)
	}
	const oldConstraint = `{"id":"pk","kind":"primary-key","columns":["amount"],"referenced_table":{"symbol":""}}`
	if string(encoded) != oldConstraint {
		t.Fatalf("legacy constraint JSON changed: %s", encoded)
	}
	operation := declaration.MigrationOperation{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "records"}}
	encoded, err = json.Marshal(operation)
	if err != nil {
		t.Fatal(err)
	}
	const oldOperation = `{"kind":"create-table","table":{"symbol":"records"},"column":{"id":"","type":"","default":{"kind":"","set":false}},"index":{"id":"","columns":null}}`
	if string(encoded) != oldOperation {
		t.Fatalf("legacy operation JSON changed: %s", encoded)
	}
}

func checkMember() declaration.Member[declaration.NoAccess] {
	constraint := func(id string) declaration.Constraint {
		return declaration.Constraint{ID: id, Kind: declaration.ConstraintCheck, Check: declaration.Predicate{Kind: declaration.PredicateAnd, Items: []declaration.Predicate{
			{Kind: declaration.PredicateGreater, Left: declaration.ColumnRef{Column: "amount"}, Value: declaration.Int64(0)},
			{Kind: declaration.PredicateOr, Items: []declaration.Predicate{
				{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "state"}, Value: declaration.String("pending")},
				{Kind: declaration.PredicateEqual, Left: declaration.ColumnRef{Column: "state"}, Value: declaration.String("active")},
			}},
		}}}
	}
	table := func() declaration.Table {
		return declaration.Table{ID: "records", Columns: []declaration.Column{{ID: "amount", Type: declaration.TypeInt64, Nullable: true}, {ID: "limit", Type: declaration.TypeInt64}, {ID: "state", Type: declaration.TypeString}}, Constraints: []declaration.Constraint{constraint("positive")}}
	}
	return declaration.Member[declaration.NoAccess]{Namespace: "checks", Tables: []declaration.Table{table()}, Migrations: []declaration.Migration{
		{ID: "initial", Version: 1, Snapshot: []declaration.MigrationSchema{{Namespace: "checks", Tables: []declaration.Table{table()}}}, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationCreateTable, Table: declaration.Ref{Symbol: "records"}}}},
		{ID: "strict", Version: 2, Predecessors: []declaration.Ref{{Symbol: "initial"}}, Snapshot: []declaration.MigrationSchema{{Namespace: "checks", Tables: []declaration.Table{table()}}}, Operations: []declaration.MigrationOperation{{Kind: declaration.MigrationAddConstraint, Table: declaration.Ref{Symbol: "records"}, Constraint: constraint("strict-positive")}}},
	}}
}
