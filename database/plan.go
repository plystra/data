package database

import (
	"fmt"
	"strings"
)

const (
	// MaximumStatements bounds one atomic plan.
	MaximumStatements = 64
	// MaximumStatementBytes bounds one generated statement.
	MaximumStatementBytes = 1 << 20
	// MaximumStatementIDBytes bounds one member-local statement identity.
	MaximumStatementIDBytes = 128
)

// Cardinality is the closed row-cardinality assertion for a query statement.
type Cardinality string

const (
	CardinalityNone       Cardinality = "none"
	CardinalityExactlyOne Cardinality = "exactly_one"
	CardinalityZeroOrOne  Cardinality = "zero_or_one"
	CardinalityMany       Cardinality = "many"
)

// AffectedRows is the closed inclusive assertion for a mutation statement.
type AffectedRows struct {
	Min       uint64
	Max       uint64
	Unbounded bool
	Set       bool
}

// ValueKind is the closed scalar vocabulary exchanged with a backend.
type ValueKind string

const (
	ValueNull    ValueKind = "null"
	ValueString  ValueKind = "string"
	ValueInt64   ValueKind = "int64"
	ValueFloat64 ValueKind = "float64"
	ValueBool    ValueKind = "bool"
	ValueBytes   ValueKind = "bytes"
)

// Argument is one typed parameter supplied to a generated statement.
type Argument struct {
	Kind    ValueKind
	String  string
	Int64   int64
	Float64 float64
	Bool    bool
	Bytes   []byte
}

func StringArgument(value string) Argument   { return Argument{Kind: ValueString, String: value} }
func Int64Argument(value int64) Argument     { return Argument{Kind: ValueInt64, Int64: value} }
func Float64Argument(value float64) Argument { return Argument{Kind: ValueFloat64, Float64: value} }
func BoolArgument(value bool) Argument       { return Argument{Kind: ValueBool, Bool: value} }
func BytesArgument(value []byte) Argument {
	return Argument{Kind: ValueBytes, Bytes: append([]byte{}, value...)}
}
func NullArgument() Argument { return Argument{Kind: ValueNull} }

// Value is one typed scalar returned by a generated query.
type Value = Argument

// ResultColumn describes one typed column returned by a query statement.
// Kind is the non-null backend value kind; Nullable controls whether NULL is
// accepted for that column.
type ResultColumn struct {
	Kind     ValueKind
	Nullable bool
}

// ExactAffectedRows creates an assertion for one exact mutation count.
func ExactAffectedRows(count uint64) AffectedRows {
	return AffectedRows{Min: count, Max: count, Set: true}
}

// AtLeastAffectedRows creates an assertion with no upper bound.
func AtLeastAffectedRows(min uint64) AffectedRows {
	return AffectedRows{Min: min, Unbounded: true, Set: true}
}

func (a AffectedRows) valid() bool {
	return a.Set && (a.Unbounded || a.Min <= a.Max)
}

func (a AffectedRows) accepts(count uint64) bool {
	return count >= a.Min && (a.Unbounded || count <= a.Max)
}

// Statement is one ordered query or mutation in an atomic plan. Generated
// Data access code supplies literal SQL and typed arguments; callers do not
// receive a transaction handle or a callback escape hatch.
type Statement struct {
	ID            string
	SQL           string
	Args          []Argument
	ReturnsRows   bool
	Cardinality   Cardinality
	Affected      AffectedRows
	ResultColumns []ResultColumn
}

// StatementResult contains only the result of one statement in the plan.
type StatementResult struct {
	Rows         [][]Value
	AffectedRows uint64
}

// Result is published only after the complete plan transaction commits.
type Result struct {
	Statements []StatementResult
}

// Plan is one finite ordered atomic database operation.
type Plan struct {
	Statements []Statement
}

// Validate checks all structural plan constraints before a transaction begins.
func (p Plan) Validate() error {
	if len(p.Statements) == 0 || len(p.Statements) > MaximumStatements {
		return fmt.Errorf("%w: statement count", ErrInvalidPlan)
	}
	seen := make(map[string]struct{}, len(p.Statements))
	for index, statement := range p.Statements {
		if err := validateStatement(index, statement, seen); err != nil {
			return err
		}
	}
	return nil
}

func validateStatement(index int, statement Statement, seen map[string]struct{}) error {
	if statement.ID == "" || len(statement.ID) > MaximumStatementIDBytes || strings.TrimSpace(statement.ID) != statement.ID {
		return fmt.Errorf("%w: statement %d identity", ErrInvalidPlan, index)
	}
	if _, exists := seen[statement.ID]; exists {
		return fmt.Errorf("%w: duplicate statement identity", ErrInvalidPlan)
	}
	seen[statement.ID] = struct{}{}
	if statement.SQL == "" || len(statement.SQL) > MaximumStatementBytes || strings.IndexByte(statement.SQL, 0) >= 0 {
		return fmt.Errorf("%w: statement %s SQL", ErrInvalidPlan, statement.ID)
	}
	for _, argument := range statement.Args {
		if argument.Kind != ValueNull && !validValueKind(argument.Kind) {
			return fmt.Errorf("%w: statement %s argument kind", ErrInvalidPlan, statement.ID)
		}
	}
	if statement.ReturnsRows {
		switch statement.Cardinality {
		case CardinalityExactlyOne, CardinalityZeroOrOne, CardinalityMany:
		default:
			return fmt.Errorf("%w: statement %s query cardinality", ErrInvalidPlan, statement.ID)
		}
		if statement.Affected != (AffectedRows{}) {
			return fmt.Errorf("%w: statement %s query assertion", ErrInvalidPlan, statement.ID)
		}
		for _, column := range statement.ResultColumns {
			if column.Kind == ValueNull || !validValueKind(column.Kind) {
				return fmt.Errorf("%w: statement %s result column", ErrInvalidPlan, statement.ID)
			}
		}
		return nil
	}
	if !cardinalityNone(statement.Cardinality) || !statement.Affected.valid() || len(statement.ResultColumns) != 0 {
		return fmt.Errorf("%w: statement %s mutation assertion", ErrInvalidPlan, statement.ID)
	}
	return nil
}

func validValueKind(value ValueKind) bool {
	switch value {
	case ValueString, ValueInt64, ValueFloat64, ValueBool, ValueBytes:
		return true
	default:
		return false
	}
}

func cardinalityNone(value Cardinality) bool {
	return value == "" || value == CardinalityNone
}

// Accepts reports whether a row count satisfies this mutation assertion.
func (a AffectedRows) Accepts(count uint64) bool {
	return a.valid() && a.accepts(count)
}
