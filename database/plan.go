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
	ID          string
	SQL         string
	Args        []any
	ReturnsRows bool
	Cardinality Cardinality
	Affected    AffectedRows
}

// StatementResult contains only the result of one statement in the plan.
type StatementResult struct {
	Rows         [][]any
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
	if statement.ReturnsRows {
		if cardinalityNone(statement.Cardinality) || statement.Affected != (AffectedRows{}) {
			return fmt.Errorf("%w: statement %s query assertion", ErrInvalidPlan, statement.ID)
		}
		return nil
	}
	if !cardinalityNone(statement.Cardinality) || !statement.Affected.valid() {
		return fmt.Errorf("%w: statement %s mutation assertion", ErrInvalidPlan, statement.ID)
	}
	return nil
}

func cardinalityNone(value Cardinality) bool {
	return value == "" || value == CardinalityNone
}

// Accepts reports whether a row count satisfies this mutation assertion.
func (a AffectedRows) Accepts(count uint64) bool {
	return a.valid() && a.accepts(count)
}
