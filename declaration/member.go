// Package declaration defines the closed, literal-friendly Data declaration
// model consumed by the Data compiler.
package declaration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	MaximumNodes       = 65536
	MaximumImports     = 1024
	MaximumSymbolBytes = 128
	MaximumNesting     = 64
)

var (
	ErrInvalid       = errors.New("invalid Data declaration")
	ErrInvalidID     = errors.New("invalid Data declaration ID")
	ErrDuplicate     = errors.New("duplicate Data declaration symbol")
	ErrReference     = errors.New("invalid Data declaration reference")
	ErrLimit         = errors.New("Data declaration limit exceeded")
	ErrUnsupported   = errors.New("unsupported Data declaration value")
	ErrInvalidSchema = errors.New("invalid relational schema")
)

// NoAccess marks a migration-only member. It is intentionally a concrete type
// so the compiler can recognize declaration.Member[declaration.NoAccess].
type NoAccess struct{}

// Ref identifies a member-local symbol. Member is empty for a local reference
// and must name an explicit import for a cross-member reference.
type Ref struct {
	Member string `json:"member,omitempty"`
	Symbol string `json:"symbol"`
}

// Member is the root literal for one versioned Data member. Access is a type
// parameter rather than a runtime value; the compiler obtains its canonical
// Resource identity from the type-checked declaration.
type Member[Access any] struct {
	Namespace  string      `json:"namespace"`
	Imports    []string    `json:"imports,omitempty"`
	Tables     []Table     `json:"tables,omitempty"`
	Queries    []Query     `json:"queries,omitempty"`
	Plans      []Plan      `json:"plans,omitempty"`
	Migrations []Migration `json:"migrations,omitempty"`
	Access     Access      `json:"-"`
}

// Model is the normalized, access-independent representation of a member.
// Slices returned by Normalize and Model accessors are owned by the result.
type Model struct {
	Namespace  string      `json:"namespace"`
	Imports    []string    `json:"imports,omitempty"`
	Tables     []Table     `json:"tables,omitempty"`
	Queries    []Query     `json:"queries,omitempty"`
	Plans      []Plan      `json:"plans,omitempty"`
	Migrations []Migration `json:"migrations,omitempty"`
}

// ScalarType is the initial backend-independent scalar vocabulary.
type ScalarType string

const (
	TypeBool      ScalarType = "bool"
	TypeBytes     ScalarType = "bytes"
	TypeFloat64   ScalarType = "float64"
	TypeInt64     ScalarType = "int64"
	TypeString    ScalarType = "string"
	TypeTimestamp ScalarType = "timestamp"
	TypeUint64    ScalarType = "uint64"
	TypeUUID      ScalarType = "uuid"
)

// Value is a closed literal value used by defaults and query expressions. A
// query value may instead name one declared typed parameter.
type Value struct {
	Kind      ScalarType `json:"kind"`
	Set       bool       `json:"set"`
	Parameter string     `json:"parameter,omitempty"`
	String    string     `json:"string,omitempty"`
	Int64     int64      `json:"int64,omitempty"`
	Uint64    uint64     `json:"uint64,omitempty"`
	Float64   float64    `json:"float64,omitempty"`
	Bool      bool       `json:"bool,omitempty"`
	Bytes     string     `json:"bytes,omitempty"`
}

func String(value string) Value   { return Value{Kind: TypeString, Set: true, String: value} }
func Int64(value int64) Value     { return Value{Kind: TypeInt64, Set: true, Int64: value} }
func Uint64(value uint64) Value   { return Value{Kind: TypeUint64, Set: true, Uint64: value} }
func Float64(value float64) Value { return Value{Kind: TypeFloat64, Set: true, Float64: value} }
func Bool(value bool) Value       { return Value{Kind: TypeBool, Set: true, Bool: value} }
func Bytes(value string) Value    { return Value{Kind: TypeBytes, Set: true, Bytes: value} }

// Parameter is one named typed query input.
type Parameter struct {
	ID   string     `json:"id"`
	Type ScalarType `json:"type"`
}

// Assignment binds one table column to a literal or typed query parameter.
type Assignment struct {
	Column string `json:"column"`
	Value  Value  `json:"value"`
}

type ConflictAction string

const (
	ConflictDoNothing ConflictAction = "do-nothing"
	ConflictDoUpdate  ConflictAction = "do-update"
)

// Conflict describes the bounded PostgreSQL conflict behavior of an insert.
// Only a named primary-key or unique constraint on the target is supported.
type Conflict struct {
	Constraint    string         `json:"constraint"`
	Action        ConflictAction `json:"action"`
	UpdateColumns []string       `json:"update_columns,omitempty"`
}

// Table is one logical relational table.
type Table struct {
	ID          string       `json:"id"`
	Columns     []Column     `json:"columns,omitempty"`
	Constraints []Constraint `json:"constraints,omitempty"`
	Indexes     []Index      `json:"indexes,omitempty"`
}

// Column is one named table column.
type Column struct {
	ID       string     `json:"id"`
	Type     ScalarType `json:"type"`
	Nullable bool       `json:"nullable,omitempty"`
	Default  Value      `json:"default,omitempty"`
}

type ConstraintKind string

const (
	ConstraintPrimaryKey ConstraintKind = "primary-key"
	ConstraintUnique     ConstraintKind = "unique"
	ConstraintForeignKey ConstraintKind = "foreign-key"
)

// Constraint describes one logical table constraint.
type Constraint struct {
	ID              string         `json:"id"`
	Kind            ConstraintKind `json:"kind"`
	Columns         []string       `json:"columns"`
	ReferencedTable Ref            `json:"referenced_table,omitempty"`
	ReferencedCols  []string       `json:"referenced_columns,omitempty"`
}

// Index describes one logical index.
type Index struct {
	ID      string   `json:"id"`
	Columns []string `json:"columns"`
	Unique  bool     `json:"unique,omitempty"`
}

type OperationKind string

const (
	OperationSelect OperationKind = "select"
	OperationInsert OperationKind = "insert"
	OperationUpdate OperationKind = "update"
	OperationDelete OperationKind = "delete"
)

// Query is deterministic typed query intent. SQL text is deliberately absent.
type Query struct {
	ID          string        `json:"id"`
	Operation   OperationKind `json:"operation"`
	Table       Ref           `json:"table"`
	Joins       []Join        `json:"joins,omitempty"`
	Parameters  []Parameter   `json:"parameters,omitempty"`
	Assignments []Assignment  `json:"assignments,omitempty"`
	Conflict    Conflict      `json:"conflict,omitempty"`
	Projection  []Projection  `json:"projection,omitempty"`
	GroupBy     []ColumnRef   `json:"group_by,omitempty"`
	Having      Predicate     `json:"having,omitempty"`
	Returning   []Projection  `json:"returning,omitempty"`
	Results     []ResultField `json:"results,omitempty"`
	Predicate   Predicate     `json:"predicate,omitempty"`
	Ordering    []Ordering    `json:"ordering,omitempty"`
	Limit       uint64        `json:"limit,omitempty"`
	HasLimit    bool          `json:"has_limit,omitempty"`
	Offset      uint64        `json:"offset,omitempty"`
	HasOffset   bool          `json:"has_offset,omitempty"`
}

func (query Query) MarshalJSON() ([]byte, error) {
	type plain Query
	var having *Predicate
	if hasHaving(query.Having) {
		having = &query.Having
	}
	return json.Marshal(struct {
		plain
		Having *Predicate `json:"having,omitempty"`
	}{plain(query), having})
}

// ColumnRef identifies one column on a table explicitly present in a query.
type ColumnRef struct {
	Table  Ref    `json:"table"`
	Column string `json:"column"`
}

type JoinKind string

const (
	JoinInner JoinKind = "inner"
	JoinLeft  JoinKind = "left"
)

// JoinCondition is one equality between already introduced and newly joined tables.
type JoinCondition struct {
	Left  ColumnRef `json:"left"`
	Right ColumnRef `json:"right"`
}

type Join struct {
	Kind  JoinKind      `json:"kind"`
	Table Ref           `json:"table"`
	On    JoinCondition `json:"on"`
}

type AggregateKind string

const (
	AggregateCount AggregateKind = "count"
	AggregateMin   AggregateKind = "min"
	AggregateMax   AggregateKind = "max"
)

type Projection struct {
	Table     Ref           `json:"table,omitempty"`
	Column    string        `json:"column,omitempty"`
	Alias     string        `json:"alias,omitempty"`
	Aggregate AggregateKind `json:"aggregate,omitempty"`
}

// ResultField maps one named query output to a typed result field.
type ResultField struct {
	ID       string     `json:"id"`
	Source   string     `json:"source"`
	Type     ScalarType `json:"type"`
	Nullable bool       `json:"nullable,omitempty"`
}

type PredicateKind string

const (
	PredicateEqual        PredicateKind = "equal"
	PredicateNotEqual     PredicateKind = "not-equal"
	PredicateLess         PredicateKind = "less"
	PredicateLessEqual    PredicateKind = "less-equal"
	PredicateGreater      PredicateKind = "greater"
	PredicateGreaterEqual PredicateKind = "greater-equal"
	PredicateIsNull       PredicateKind = "is-null"
	PredicateIsNotNull    PredicateKind = "is-not-null"
	PredicateAnd          PredicateKind = "and"
	PredicateOr           PredicateKind = "or"
)

type Predicate struct {
	Kind  PredicateKind `json:"kind"`
	Left  ColumnRef     `json:"left,omitempty"`
	Right ColumnRef     `json:"right,omitempty"`
	Value Value         `json:"value,omitempty"`
	Items []Predicate   `json:"items,omitempty"`
}

type OrderingDirection string

const (
	OrderingAscending  OrderingDirection = "asc"
	OrderingDescending OrderingDirection = "desc"
)

type Ordering struct {
	Table     Ref               `json:"table,omitempty"`
	Column    string            `json:"column"`
	Direction OrderingDirection `json:"direction"`
}

// Plan is one ordered atomic statement plan.
type Plan struct {
	ID         string          `json:"id"`
	Statements []PlanStatement `json:"statements"`
}

type PlanStatement struct {
	ID          string       `json:"id"`
	Query       Ref          `json:"query"`
	Cardinality Cardinality  `json:"cardinality"`
	Affected    AffectedRows `json:"affected,omitempty"`
}

type Cardinality string

const (
	CardinalityNone       Cardinality = "none"
	CardinalityExactlyOne Cardinality = "exactly_one"
	CardinalityZeroOrOne  Cardinality = "zero_or_one"
	CardinalityMany       Cardinality = "many"
)

type AffectedRows struct {
	Set       bool   `json:"set"`
	Min       uint64 `json:"min"`
	Max       uint64 `json:"max,omitempty"`
	Unbounded bool   `json:"unbounded,omitempty"`
}

// Migration is immutable logical migration intent.
type Migration struct {
	ID           string               `json:"id"`
	Version      uint64               `json:"version"`
	Predecessors []Ref                `json:"predecessors,omitempty"`
	Operations   []MigrationOperation `json:"operations"`
}

type MigrationOperationKind string

const (
	MigrationCreateTable MigrationOperationKind = "create-table"
	MigrationAddColumn   MigrationOperationKind = "add-column"
	MigrationCreateIndex MigrationOperationKind = "create-index"
)

type MigrationOperation struct {
	Kind   MigrationOperationKind `json:"kind"`
	Table  Ref                    `json:"table"`
	Column Column                 `json:"column,omitempty"`
	Index  Index                  `json:"index,omitempty"`
}

// Normalize validates the closed model and returns a deterministic copy.
func (m Member[Access]) Normalize() (Model, error) {
	model := Model{
		Namespace:  m.Namespace,
		Imports:    append([]string(nil), m.Imports...),
		Tables:     cloneTables(m.Tables),
		Queries:    cloneQueries(m.Queries),
		Plans:      clonePlans(m.Plans),
		Migrations: cloneMigrations(m.Migrations),
	}
	if err := validateModel(&model); err != nil {
		return Model{}, err
	}
	sort.Strings(model.Imports)
	sort.Slice(model.Tables, func(i, j int) bool { return model.Tables[i].ID < model.Tables[j].ID })
	sort.Slice(model.Queries, func(i, j int) bool { return model.Queries[i].ID < model.Queries[j].ID })
	sort.Slice(model.Plans, func(i, j int) bool { return model.Plans[i].ID < model.Plans[j].ID })
	sort.Slice(model.Migrations, func(i, j int) bool {
		if model.Migrations[i].Version != model.Migrations[j].Version {
			return model.Migrations[i].Version < model.Migrations[j].Version
		}
		return model.Migrations[i].ID < model.Migrations[j].ID
	})
	for i := range model.Tables {
		sort.Slice(model.Tables[i].Columns, func(a, b int) bool { return model.Tables[i].Columns[a].ID < model.Tables[i].Columns[b].ID })
		sort.Slice(model.Tables[i].Constraints, func(a, b int) bool { return model.Tables[i].Constraints[a].ID < model.Tables[i].Constraints[b].ID })
		sort.Slice(model.Tables[i].Indexes, func(a, b int) bool { return model.Tables[i].Indexes[a].ID < model.Tables[i].Indexes[b].ID })
	}
	for i := range model.Queries {
		sort.Slice(model.Queries[i].Parameters, func(a, b int) bool { return model.Queries[i].Parameters[a].ID < model.Queries[i].Parameters[b].ID })
		sort.Slice(model.Queries[i].Assignments, func(a, b int) bool {
			return model.Queries[i].Assignments[a].Column < model.Queries[i].Assignments[b].Column
		})
		sort.Strings(model.Queries[i].Conflict.UpdateColumns)
	}
	for i := range model.Migrations {
		sort.Slice(model.Migrations[i].Predecessors, func(a, b int) bool {
			return refKey(model.Migrations[i].Predecessors[a]) < refKey(model.Migrations[i].Predecessors[b])
		})
	}
	return model, nil
}

// Digest returns a domain-separated digest of the normalized member model.
func (m Member[Access]) Digest() (string, error) {
	model, err := m.Normalize()
	if err != nil {
		return "", err
	}
	return DigestModel(model)
}

// DigestModel returns a domain-separated digest of an already normalized
// logical model. Callers should prefer Member.Digest when they have a root.
func DigestModel(model Model) (string, error) {
	payload, err := json.Marshal(model)
	if err != nil {
		return "", fmt.Errorf("%w: marshal normalized model: %v", ErrInvalid, err)
	}
	sum := sha256.Sum256(append([]byte("plystra.data.logical-model/v1\x00"), payload...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func cloneTables(tables []Table) []Table {
	result := make([]Table, len(tables))
	for i, table := range tables {
		result[i] = table
		result[i].Columns = append([]Column(nil), table.Columns...)
		for column := range result[i].Columns {
			result[i].Columns[column].Default.Bytes = table.Columns[column].Default.Bytes
		}
		result[i].Constraints = make([]Constraint, len(table.Constraints))
		for constraint, value := range table.Constraints {
			result[i].Constraints[constraint] = value
			result[i].Constraints[constraint].Columns = append([]string(nil), value.Columns...)
			result[i].Constraints[constraint].ReferencedCols = append([]string(nil), value.ReferencedCols...)
		}
		result[i].Indexes = make([]Index, len(table.Indexes))
		for index, value := range table.Indexes {
			result[i].Indexes[index] = value
			result[i].Indexes[index].Columns = append([]string(nil), value.Columns...)
		}
	}
	return result
}

func cloneQueries(queries []Query) []Query {
	result := make([]Query, len(queries))
	for i, query := range queries {
		result[i] = query
		result[i].Joins = append([]Join(nil), query.Joins...)
		result[i].Parameters = append([]Parameter(nil), query.Parameters...)
		result[i].Assignments = make([]Assignment, len(query.Assignments))
		for assignment := range query.Assignments {
			result[i].Assignments[assignment] = query.Assignments[assignment]
		}
		result[i].Conflict.UpdateColumns = append([]string(nil), query.Conflict.UpdateColumns...)
		result[i].Projection = append([]Projection(nil), query.Projection...)
		result[i].GroupBy = append([]ColumnRef(nil), query.GroupBy...)
		result[i].Having = clonePredicate(query.Having)
		result[i].Returning = append([]Projection(nil), query.Returning...)
		result[i].Results = append([]ResultField(nil), query.Results...)
		result[i].Ordering = append([]Ordering(nil), query.Ordering...)
		result[i].Predicate = clonePredicate(query.Predicate)
	}
	return result
}

func clonePredicate(predicate Predicate) Predicate {
	if predicate.Items != nil {
		items := predicate.Items
		predicate.Items = make([]Predicate, len(items))
		for i, item := range items {
			predicate.Items[i] = clonePredicate(item)
		}
	}
	return predicate
}

func clonePlans(plans []Plan) []Plan {
	result := make([]Plan, len(plans))
	for i, plan := range plans {
		result[i] = plan
		result[i].Statements = append([]PlanStatement(nil), plan.Statements...)
	}
	return result
}

func cloneMigrations(migrations []Migration) []Migration {
	result := make([]Migration, len(migrations))
	for i, migration := range migrations {
		result[i] = migration
		result[i].Predecessors = append([]Ref(nil), migration.Predecessors...)
		result[i].Operations = make([]MigrationOperation, len(migration.Operations))
		for operation, value := range migration.Operations {
			result[i].Operations[operation] = value
			result[i].Operations[operation].Column.Default.Bytes = value.Column.Default.Bytes
			result[i].Operations[operation].Index.Columns = append([]string(nil), value.Index.Columns...)
		}
	}
	return result
}

func validateModel(model *Model) error {
	if !validNamespace(model.Namespace) {
		return fmt.Errorf("%w: namespace %q", ErrInvalidSchema, model.Namespace)
	}
	if len(model.Imports) > MaximumImports {
		return ErrLimit
	}
	imports := make(map[string]struct{}, len(model.Imports))
	for _, imported := range model.Imports {
		if !validMemberID(imported) || imported == model.Namespace {
			return fmt.Errorf("%w: import %q", ErrInvalidID, imported)
		}
		if _, exists := imports[imported]; exists {
			return fmt.Errorf("%w: import %q", ErrDuplicate, imported)
		}
		imports[imported] = struct{}{}
	}
	if nodeCount(*model) > MaximumNodes {
		return ErrLimit
	}
	symbols := map[string]string{}
	tables := make(map[string]Table, len(model.Tables))
	for _, table := range model.Tables {
		if err := addSymbol(symbols, table.ID, "table"); err != nil {
			return err
		}
		tables[table.ID] = table
		if err := validateTable(table); err != nil {
			return err
		}
	}
	queries := make(map[string]Query, len(model.Queries))
	for _, query := range model.Queries {
		if err := addSymbol(symbols, query.ID, "query"); err != nil {
			return err
		}
		queries[query.ID] = query
		if err := validateQuery(query, imports, tables); err != nil {
			return err
		}
	}
	for _, plan := range model.Plans {
		if err := addSymbol(symbols, plan.ID, "plan"); err != nil {
			return err
		}
		if err := validatePlan(plan, imports, queries); err != nil {
			return err
		}
	}
	for _, migration := range model.Migrations {
		if err := addSymbol(symbols, migration.ID, "migration"); err != nil {
			return err
		}
		if migration.Version == 0 || len(migration.Operations) == 0 {
			return fmt.Errorf("%w: migration %q", ErrInvalidSchema, migration.ID)
		}
		for _, predecessor := range migration.Predecessors {
			if err := validateRef(predecessor, imports); err != nil {
				return err
			}
		}
		for _, predecessor := range migration.Predecessors {
			if predecessor.Member == "" {
				if _, exists := symbols[predecessor.Symbol]; !exists {
					return fmt.Errorf("%w: migration %q", ErrReference, predecessor.Symbol)
				}
			}
		}
		for _, operation := range migration.Operations {
			if operation.Kind != MigrationCreateTable && operation.Kind != MigrationAddColumn && operation.Kind != MigrationCreateIndex {
				return fmt.Errorf("%w: migration operation %q", ErrUnsupported, operation.Kind)
			}
			if err := validateRef(operation.Table, imports); err != nil {
				return err
			}
			if operation.Table.Member == "" {
				if _, exists := tables[operation.Table.Symbol]; !exists {
					return fmt.Errorf("%w: migration table %q", ErrReference, operation.Table.Symbol)
				}
			}
		}
	}
	return nil
}

func validateTable(table Table) error {
	if !validSymbol(table.ID) {
		return fmt.Errorf("%w: table %q", ErrInvalidID, table.ID)
	}
	columns := map[string]struct{}{}
	for _, column := range table.Columns {
		if !validSymbol(column.ID) || !validScalar(column.Type) {
			return fmt.Errorf("%w: column %q", ErrInvalidSchema, column.ID)
		}
		if _, exists := columns[column.ID]; exists {
			return fmt.Errorf("%w: column %q", ErrDuplicate, column.ID)
		}
		columns[column.ID] = struct{}{}
		if err := validateValue(column.Default); err != nil {
			return err
		}
	}
	constraints := map[string]struct{}{}
	for _, constraint := range table.Constraints {
		if !validSymbol(constraint.ID) || len(constraint.Columns) == 0 {
			return fmt.Errorf("%w: constraint %q", ErrInvalidSchema, constraint.ID)
		}
		if _, exists := constraints[constraint.ID]; exists {
			return fmt.Errorf("%w: constraint %q", ErrDuplicate, constraint.ID)
		}
		constraints[constraint.ID] = struct{}{}
		if constraint.Kind != ConstraintPrimaryKey && constraint.Kind != ConstraintUnique && constraint.Kind != ConstraintForeignKey {
			return fmt.Errorf("%w: constraint %q", ErrUnsupported, constraint.Kind)
		}
		for _, column := range constraint.Columns {
			if _, exists := columns[column]; !exists {
				return fmt.Errorf("%w: constraint column %q", ErrReference, column)
			}
		}
		if constraint.Kind == ConstraintForeignKey {
			if err := validateRef(constraint.ReferencedTable, nil); err != nil || len(constraint.ReferencedCols) == 0 {
				return fmt.Errorf("%w: foreign key %q", ErrReference, constraint.ID)
			}
		}
	}
	for _, index := range table.Indexes {
		if !validSymbol(index.ID) || len(index.Columns) == 0 {
			return fmt.Errorf("%w: index %q", ErrInvalidSchema, index.ID)
		}
		for _, column := range index.Columns {
			if _, exists := columns[column]; !exists {
				return fmt.Errorf("%w: index column %q", ErrReference, column)
			}
		}
	}
	return nil
}

func validateQuery(query Query, imports map[string]struct{}, tables map[string]Table) error {
	if !validSymbol(query.ID) || !validOperation(query.Operation) || query.Table.Symbol == "" {
		return fmt.Errorf("%w: query %q", ErrInvalidSchema, query.ID)
	}
	if err := validateRef(query.Table, imports); err != nil {
		return err
	}
	if query.Table.Member == "" {
		if _, exists := tables[query.Table.Symbol]; !exists {
			return fmt.Errorf("%w: table %q", ErrReference, query.Table.Symbol)
		}
	}
	parameters := make(map[string]ScalarType, len(query.Parameters))
	if len(query.Joins) != 0 && query.Operation != OperationSelect {
		return fmt.Errorf("%w: joins require select query", ErrInvalidSchema)
	}
	introduced := map[string]struct{}{refKey(query.Table): {}}
	for _, join := range query.Joins {
		if join.Kind != JoinInner && join.Kind != JoinLeft {
			return fmt.Errorf("%w: join kind %q", ErrUnsupported, join.Kind)
		}
		if err := validateRef(join.Table, imports); err != nil {
			return err
		}
		if join.Table.Member == "" {
			if _, exists := tables[join.Table.Symbol]; !exists {
				return fmt.Errorf("%w: join table %q", ErrReference, join.Table.Symbol)
			}
		}
		key := refKey(join.Table)
		if _, exists := introduced[key]; exists {
			return fmt.Errorf("%w: join table %q", ErrDuplicate, join.Table.Symbol)
		}
		if err := validateRef(join.On.Left.Table, imports); err != nil {
			return err
		}
		if _, exists := introduced[refKey(join.On.Left.Table)]; !exists || !validSymbol(join.On.Left.Column) {
			return fmt.Errorf("%w: join left column %q", ErrReference, join.On.Left.Column)
		}
		if join.On.Right.Table != join.Table || !validSymbol(join.On.Right.Column) {
			return fmt.Errorf("%w: join right column %q", ErrReference, join.On.Right.Column)
		}
		introduced[key] = struct{}{}
	}
	for _, parameter := range query.Parameters {
		if !validSymbol(parameter.ID) || !validScalar(parameter.Type) {
			return fmt.Errorf("%w: query parameter %q", ErrInvalidSchema, parameter.ID)
		}
		if _, exists := parameters[parameter.ID]; exists {
			return fmt.Errorf("%w: query parameter %q", ErrDuplicate, parameter.ID)
		}
		parameters[parameter.ID] = parameter.Type
	}
	switch query.Operation {
	case OperationSelect:
		if len(query.Assignments) != 0 || len(query.Returning) != 0 {
			return fmt.Errorf("%w: select query assignments", ErrInvalidSchema)
		}
	case OperationInsert:
		if len(query.Predicate.Items) != 0 || query.Predicate.Kind != "" || len(query.Ordering) != 0 || len(query.GroupBy) != 0 || query.HasLimit || query.HasOffset || len(query.Projection) != 0 {
			return fmt.Errorf("%w: insert query shape", ErrInvalidSchema)
		}
	case OperationUpdate:
		if len(query.Assignments) == 0 || query.Predicate.Kind == "" || len(query.Ordering) != 0 || len(query.GroupBy) != 0 || query.HasLimit || query.HasOffset || len(query.Projection) != 0 {
			return fmt.Errorf("%w: update query shape", ErrInvalidSchema)
		}
	case OperationDelete:
		if len(query.Assignments) != 0 || query.Predicate.Kind == "" || len(query.Ordering) != 0 || len(query.GroupBy) != 0 || query.HasLimit || query.HasOffset || len(query.Projection) != 0 {
			return fmt.Errorf("%w: delete query shape", ErrInvalidSchema)
		}
	}
	if query.Conflict.Action != "" || query.Conflict.Constraint != "" || len(query.Conflict.UpdateColumns) != 0 {
		if query.Operation != OperationInsert || !validSymbol(query.Conflict.Constraint) ||
			(query.Conflict.Action != ConflictDoNothing && query.Conflict.Action != ConflictDoUpdate) ||
			(query.Conflict.Action == ConflictDoNothing && len(query.Conflict.UpdateColumns) != 0) ||
			(query.Conflict.Action == ConflictDoUpdate && len(query.Conflict.UpdateColumns) == 0) {
			return fmt.Errorf("%w: query conflict", ErrInvalidSchema)
		}
		if query.Table.Member == "" {
			found := false
			for _, constraint := range tables[query.Table.Symbol].Constraints {
				if constraint.ID == query.Conflict.Constraint && (constraint.Kind == ConstraintPrimaryKey || constraint.Kind == ConstraintUnique) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%w: conflict constraint %q", ErrReference, query.Conflict.Constraint)
			}
		}
	}
	seenAssignments := make(map[string]struct{}, len(query.Assignments))
	for _, assignment := range query.Assignments {
		if !validSymbol(assignment.Column) {
			return fmt.Errorf("%w: query assignment %q", ErrInvalidSchema, assignment.Column)
		}
		if _, exists := seenAssignments[assignment.Column]; exists {
			return fmt.Errorf("%w: query assignment %q", ErrDuplicate, assignment.Column)
		}
		seenAssignments[assignment.Column] = struct{}{}
		if err := validateQueryValue(assignment.Value, parameters); err != nil {
			return fmt.Errorf("%w: assignment %s: %v", ErrInvalidSchema, assignment.Column, err)
		}
	}
	seenUpdates := make(map[string]struct{}, len(query.Conflict.UpdateColumns))
	for _, column := range query.Conflict.UpdateColumns {
		if !validSymbol(column) {
			return fmt.Errorf("%w: conflict update column %q", ErrInvalidSchema, column)
		}
		if _, exists := seenUpdates[column]; exists {
			return fmt.Errorf("%w: conflict update column %q", ErrDuplicate, column)
		}
		seenUpdates[column] = struct{}{}
		if _, exists := seenAssignments[column]; !exists {
			return fmt.Errorf("%w: conflict update column %q has no insert assignment", ErrReference, column)
		}
		if query.Table.Member == "" {
			found := false
			for _, targetColumn := range tables[query.Table.Symbol].Columns {
				if targetColumn.ID == column {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("%w: conflict update column %q", ErrReference, column)
			}
		}
	}
	outputs := query.Projection
	if query.Operation != OperationSelect {
		outputs = query.Returning
	}
	if len(query.Results) != len(outputs) {
		return fmt.Errorf("%w: query result mapping", ErrInvalidSchema)
	}
	aggregateOutputs := 0
	for _, projection := range outputs {
		if projection.Aggregate != "" {
			aggregateOutputs++
		}
	}
	if aggregateOutputs != 0 && (aggregateOutputs != 1 || len(outputs) != 1) {
		if len(query.GroupBy) == 0 || aggregateOutputs != 1 || len(outputs) != len(query.GroupBy)+1 {
			return fmt.Errorf("%w: aggregate query projection", ErrUnsupported)
		}
	}
	if len(query.GroupBy) != 0 && (query.Operation != OperationSelect || len(query.Joins) != 0 || aggregateOutputs != 1 || len(outputs) != len(query.GroupBy)+1) {
		return fmt.Errorf("%w: grouped query shape", ErrUnsupported)
	}
	havingPresent := hasHaving(query.Having)
	if havingPresent && (query.Operation != OperationSelect || len(query.GroupBy) == 0) {
		return fmt.Errorf("%w: having query shape", ErrUnsupported)
	}
	if havingPresent && query.Having.Kind == "" {
		return fmt.Errorf("%w: having predicate kind", ErrInvalidSchema)
	}
	if aggregateOutputs == 0 && len(query.GroupBy) != 0 {
		return fmt.Errorf("%w: grouped query aggregate", ErrUnsupported)
	}
	if aggregateOutputs != 0 && len(query.Results) != len(outputs) {
		return fmt.Errorf("%w: aggregate query result mapping", ErrInvalidSchema)
	}
	if aggregateOutputs != 0 {
		for _, projection := range outputs {
			if projection.Aggregate == "" {
				continue
			}
			var result ResultField
			found := false
			for _, candidate := range query.Results {
				if candidate.Source == projection.Alias {
					result = candidate
					found = true
					break
				}
			}
			wantNullable := projection.Aggregate == AggregateMin || projection.Aggregate == AggregateMax
			if !found || result.Type != TypeInt64 || result.Nullable != wantNullable {
				return fmt.Errorf("%w: aggregate query result type", ErrInvalidSchema)
			}
		}
	}
	if aggregateOutputs != 0 && (len(query.Ordering) != 0 || query.HasLimit || query.HasOffset) {
		return fmt.Errorf("%w: aggregate query modifiers", ErrUnsupported)
	}
	for _, projection := range outputs {
		if projection.Aggregate == "" {
			if !validSymbol(projection.Column) {
				return fmt.Errorf("%w: query projection %q", ErrInvalidSchema, projection.Column)
			}
		} else {
			if query.Operation != OperationSelect || (projection.Aggregate != AggregateCount && projection.Aggregate != AggregateMin && projection.Aggregate != AggregateMax) {
				return fmt.Errorf("%w: query aggregate %q", ErrUnsupported, projection.Aggregate)
			}
			if projection.Column != "" && !validSymbol(projection.Column) {
				return fmt.Errorf("%w: query aggregate column %q", ErrInvalidSchema, projection.Column)
			}
			if (projection.Aggregate == AggregateMin || projection.Aggregate == AggregateMax) && projection.Column == "" {
				return fmt.Errorf("%w: %s aggregate column", ErrInvalidSchema, projection.Aggregate)
			}
			if (projection.Aggregate == AggregateMin || projection.Aggregate == AggregateMax) && projection.Table.Member == "" {
				target := query.Table.Symbol
				if projection.Table.Symbol != "" {
					target = projection.Table.Symbol
				}
				if table, exists := tables[target]; exists {
					for _, column := range table.Columns {
						if column.ID == projection.Column && column.Type != TypeInt64 {
							return fmt.Errorf("%w: %s aggregate column type", ErrInvalidSchema, projection.Aggregate)
						}
					}
				}
			}
			if projection.Column == "" && (projection.Table.Member != "" || projection.Table.Symbol != "") {
				return fmt.Errorf("%w: count star table", ErrInvalidSchema)
			}
			if projection.Alias == "" {
				return fmt.Errorf("%w: aggregate projection alias", ErrInvalidSchema)
			}
		}
		if projection.Alias != "" && !validSymbol(projection.Alias) {
			return fmt.Errorf("%w: query projection %q", ErrInvalidSchema, projection.Column)
		}
		if projection.Table.Member != "" || projection.Table.Symbol != "" {
			if err := validateRef(projection.Table, imports); err != nil {
				return err
			}
			if _, exists := introduced[refKey(projection.Table)]; !exists {
				return fmt.Errorf("%w: query projection table %q", ErrReference, projection.Table.Symbol)
			}
		}
	}
	if len(query.GroupBy) != 0 {
		for index, group := range query.GroupBy {
			if err := validatePredicateColumn(group, imports, introduced, query.Table); err != nil {
				return fmt.Errorf("group by: %w", err)
			}
			for _, previous := range query.GroupBy[:index] {
				if sameColumnRef(previous, group, query.Table) {
					return fmt.Errorf("%w: group by column", ErrDuplicate)
				}
			}
			matched := false
			for _, projection := range outputs {
				if projection.Aggregate == "" && sameColumnRef(ColumnRef{Table: projection.Table, Column: projection.Column}, group, query.Table) {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("%w: group by projection", ErrReference)
			}
		}
	}
	if havingPresent {
		outputsByName := make(map[string]struct{}, len(outputs))
		for _, projection := range outputs {
			name := projection.Column
			if projection.Alias != "" {
				name = projection.Alias
			}
			outputsByName[name] = struct{}{}
		}
		if err := validateHavingPredicate(query.Having, parameters, outputsByName, 0); err != nil {
			return err
		}
	}
	if len(query.Results) != 0 {
		outputNames := make(map[string]struct{}, len(outputs))
		for _, projection := range outputs {
			name := projection.Column
			if projection.Alias != "" {
				name = projection.Alias
			}
			if _, exists := outputNames[name]; exists {
				return fmt.Errorf("%w: query output %q", ErrDuplicate, name)
			}
			outputNames[name] = struct{}{}
		}
		resultIDs := make(map[string]struct{}, len(query.Results))
		resultSources := make(map[string]struct{}, len(query.Results))
		for _, result := range query.Results {
			if !validSymbol(result.ID) || !validSymbol(result.Source) || !validScalar(result.Type) {
				return fmt.Errorf("%w: query result %q", ErrInvalidSchema, result.ID)
			}
			if _, exists := resultIDs[result.ID]; exists {
				return fmt.Errorf("%w: query result %q", ErrDuplicate, result.ID)
			}
			resultIDs[result.ID] = struct{}{}
			if _, exists := resultSources[result.Source]; exists {
				return fmt.Errorf("%w: query result source %q", ErrDuplicate, result.Source)
			}
			resultSources[result.Source] = struct{}{}
			if _, exists := outputNames[result.Source]; !exists {
				return fmt.Errorf("%w: query result source %q", ErrReference, result.Source)
			}
		}
	}
	if err := validatePredicate(query.Predicate, imports, parameters, introduced, query.Table, 0); err != nil {
		return err
	}
	for _, ordering := range query.Ordering {
		if !validSymbol(ordering.Column) || ordering.Direction != OrderingAscending && ordering.Direction != OrderingDescending {
			return fmt.Errorf("%w: query ordering %q", ErrInvalidSchema, ordering.Column)
		}
		if ordering.Table.Member != "" || ordering.Table.Symbol != "" {
			if err := validateRef(ordering.Table, imports); err != nil {
				return err
			}
			if _, exists := introduced[refKey(ordering.Table)]; !exists {
				return fmt.Errorf("%w: query ordering table %q", ErrReference, ordering.Table.Symbol)
			}
		}
	}
	return nil
}

func validatePlan(plan Plan, imports map[string]struct{}, queries map[string]Query) error {
	if !validSymbol(plan.ID) || len(plan.Statements) == 0 || len(plan.Statements) > 64 {
		return fmt.Errorf("%w: plan %q", ErrInvalidSchema, plan.ID)
	}
	seen := map[string]struct{}{}
	for _, statement := range plan.Statements {
		if !validSymbol(statement.ID) {
			return fmt.Errorf("%w: plan statement %q", ErrInvalidID, statement.ID)
		}
		if _, exists := seen[statement.ID]; exists {
			return fmt.Errorf("%w: plan statement %q", ErrDuplicate, statement.ID)
		}
		seen[statement.ID] = struct{}{}
		if err := validateRef(statement.Query, imports); err != nil {
			return err
		}
		if statement.Query.Member == "" {
			query, exists := queries[statement.Query.Symbol]
			if !exists {
				return fmt.Errorf("%w: query %q", ErrReference, statement.Query.Symbol)
			}
			switch query.Operation {
			case OperationSelect:
				if statement.Cardinality == CardinalityNone || statement.Affected.Set {
					return fmt.Errorf("%w: select plan statement %q assertion", ErrInvalidSchema, statement.ID)
				}
			case OperationInsert, OperationUpdate, OperationDelete:
				if len(query.Returning) != 0 {
					return fmt.Errorf("%w: mutation plan statement %q returning", ErrUnsupported, statement.ID)
				}
				if statement.Cardinality != CardinalityNone || !statement.Affected.Set {
					return fmt.Errorf("%w: mutation plan statement %q assertion", ErrInvalidSchema, statement.ID)
				}
			}
		}
		if statement.Cardinality == "" {
			return fmt.Errorf("%w: plan statement %q cardinality", ErrInvalidSchema, statement.ID)
		}
		if statement.Cardinality != CardinalityNone && statement.Cardinality != CardinalityExactlyOne && statement.Cardinality != CardinalityZeroOrOne && statement.Cardinality != CardinalityMany {
			return fmt.Errorf("%w: cardinality %q", ErrUnsupported, statement.Cardinality)
		}
		if statement.Affected.Set && !statement.Affected.Unbounded && statement.Affected.Min > statement.Affected.Max {
			return fmt.Errorf("%w: affected row range", ErrInvalidSchema)
		}
	}
	return nil
}

func validatePredicate(predicate Predicate, imports map[string]struct{}, parameters map[string]ScalarType, introduced map[string]struct{}, base Ref, depth int) error {
	if predicate.Kind == "" {
		if predicate.Left != (ColumnRef{}) || predicate.Right != (ColumnRef{}) || predicate.Value != (Value{}) || predicate.Items != nil {
			return fmt.Errorf("%w: predicate has fields without a kind", ErrInvalidSchema)
		}
		return nil
	}
	if depth > MaximumNesting {
		return ErrLimit
	}
	switch predicate.Kind {
	case PredicateEqual, PredicateNotEqual, PredicateLess, PredicateLessEqual, PredicateGreater, PredicateGreaterEqual:
		if predicate.Items != nil {
			return fmt.Errorf("%w: predicate comparison has nested items", ErrInvalidSchema)
		}
		if err := validatePredicateColumn(predicate.Left, imports, introduced, base); err != nil {
			return err
		}
		rightSet := predicateColumnSet(predicate.Right)
		if rightSet == predicate.Value.Set {
			return fmt.Errorf("%w: predicate requires exactly one right operand", ErrInvalidSchema)
		}
		if rightSet {
			if predicate.Value != (Value{}) {
				return fmt.Errorf("%w: predicate comparison has an unused value", ErrInvalidSchema)
			}
			return validatePredicateColumn(predicate.Right, imports, introduced, base)
		}
		return validateQueryValue(predicate.Value, parameters)
	case PredicateIsNull, PredicateIsNotNull:
		if predicate.Items != nil {
			return fmt.Errorf("%w: null predicate has nested items", ErrInvalidSchema)
		}
		if err := validatePredicateColumn(predicate.Left, imports, introduced, base); err != nil {
			return err
		}
		if predicate.Right != (ColumnRef{}) || predicate.Value != (Value{}) {
			return fmt.Errorf("%w: null predicate has an operand", ErrInvalidSchema)
		}
	case PredicateAnd, PredicateOr:
		if predicate.Left != (ColumnRef{}) || predicate.Right != (ColumnRef{}) || predicate.Value != (Value{}) {
			return fmt.Errorf("%w: boolean predicate has operands", ErrInvalidSchema)
		}
		if len(predicate.Items) < 2 {
			return fmt.Errorf("%w: predicate requires two items", ErrInvalidSchema)
		}
		for _, item := range predicate.Items {
			if err := validatePredicate(item, imports, parameters, introduced, base, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: predicate %q", ErrUnsupported, predicate.Kind)
	}
	return nil
}

func validateHavingPredicate(predicate Predicate, parameters map[string]ScalarType, outputs map[string]struct{}, depth int) error {
	if depth > MaximumNesting {
		return ErrLimit
	}
	validateOutput := func(ref ColumnRef) error {
		if ref.Table.Member != "" || ref.Table.Symbol != "" {
			return fmt.Errorf("%w: having columns cannot name a table", ErrUnsupported)
		}
		if !validSymbol(ref.Column) {
			return fmt.Errorf("%w: having column %q", ErrReference, ref.Column)
		}
		if _, exists := outputs[ref.Column]; !exists {
			return fmt.Errorf("%w: having output %q", ErrReference, ref.Column)
		}
		return nil
	}
	switch predicate.Kind {
	case PredicateEqual, PredicateNotEqual, PredicateLess, PredicateLessEqual, PredicateGreater, PredicateGreaterEqual:
		if predicate.Items != nil {
			return fmt.Errorf("%w: having comparison has nested items", ErrInvalidSchema)
		}
		if err := validateOutput(predicate.Left); err != nil {
			return err
		}
		rightSet := predicateColumnSet(predicate.Right)
		if rightSet == predicate.Value.Set {
			return fmt.Errorf("%w: having requires exactly one right operand", ErrInvalidSchema)
		}
		if rightSet {
			return validateOutput(predicate.Right)
		}
		return validateQueryValue(predicate.Value, parameters)
	case PredicateIsNull, PredicateIsNotNull:
		if predicate.Items != nil {
			return fmt.Errorf("%w: having null predicate has nested items", ErrInvalidSchema)
		}
		if err := validateOutput(predicate.Left); err != nil {
			return err
		}
		if predicateColumnSet(predicate.Right) || predicate.Value.Set {
			return fmt.Errorf("%w: having null predicate has an operand", ErrInvalidSchema)
		}
	case PredicateAnd, PredicateOr:
		if predicate.Left != (ColumnRef{}) || predicate.Right != (ColumnRef{}) || predicate.Value != (Value{}) {
			return fmt.Errorf("%w: having boolean predicate has operands", ErrInvalidSchema)
		}
		if len(predicate.Items) < 2 {
			return fmt.Errorf("%w: having predicate requires two items", ErrInvalidSchema)
		}
		for _, item := range predicate.Items {
			if err := validateHavingPredicate(item, parameters, outputs, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: having predicate %q", ErrUnsupported, predicate.Kind)
	}
	return nil
}

func hasHaving(predicate Predicate) bool {
	return predicate.Kind != "" || predicate.Left != (ColumnRef{}) || predicate.Right != (ColumnRef{}) || predicate.Value != (Value{}) || predicate.Items != nil
}

func validatePredicateColumn(ref ColumnRef, imports map[string]struct{}, introduced map[string]struct{}, base Ref) error {
	if ref.Table.Member == "" && ref.Table.Symbol == "" {
		ref.Table = base
	}
	if err := validateRef(ref.Table, imports); err != nil {
		return err
	}
	if _, exists := introduced[refKey(ref.Table)]; !exists {
		return fmt.Errorf("%w: predicate table %q", ErrReference, ref.Table.Symbol)
	}
	if !validSymbol(ref.Column) {
		return fmt.Errorf("%w: predicate column %q", ErrReference, ref.Column)
	}
	return nil
}

func sameColumnRef(left, right ColumnRef, base Ref) bool {
	if left.Table.Member == "" && left.Table.Symbol == "" {
		left.Table = base
	}
	if right.Table.Member == "" && right.Table.Symbol == "" {
		right.Table = base
	}
	return left == right
}

func predicateColumnSet(ref ColumnRef) bool {
	return ref.Table.Member != "" || ref.Table.Symbol != "" || ref.Column != ""
}

func validateValue(value Value) error {
	if value.Parameter != "" {
		return fmt.Errorf("%w: parameter reference is not a literal", ErrUnsupported)
	}
	if !value.Set {
		return nil
	}
	if !validScalar(value.Kind) || value.Kind == TypeUUID || value.Kind == TypeTimestamp {
		return fmt.Errorf("%w: value kind %q", ErrUnsupported, value.Kind)
	}
	if value.Kind == TypeString && !utf8.ValidString(value.String) {
		return fmt.Errorf("%w: value is not UTF-8", ErrInvalidSchema)
	}
	return nil
}

func validateQueryValue(value Value, parameters map[string]ScalarType) error {
	if !value.Set {
		return fmt.Errorf("%w: query value is missing", ErrInvalidSchema)
	}
	if value.Parameter == "" {
		return validateValue(value)
	}
	if !validSymbol(value.Parameter) || !validScalar(value.Kind) {
		return fmt.Errorf("%w: invalid parameter reference", ErrInvalidSchema)
	}
	if expected, exists := parameters[value.Parameter]; !exists || expected != value.Kind {
		return fmt.Errorf("%w: parameter %q type", ErrReference, value.Parameter)
	}
	if value.String != "" || value.Int64 != 0 || value.Uint64 != 0 || value.Float64 != 0 || value.Bool || value.Bytes != "" {
		return fmt.Errorf("%w: parameter reference carries literal data", ErrInvalidSchema)
	}
	return nil
}

func validateRef(ref Ref, imports map[string]struct{}) error {
	if !validSymbol(ref.Symbol) {
		return fmt.Errorf("%w: symbol %q", ErrReference, ref.Symbol)
	}
	if ref.Member != "" {
		if !validMemberID(ref.Member) {
			return fmt.Errorf("%w: member %q", ErrReference, ref.Member)
		}
		if imports != nil {
			if _, ok := imports[ref.Member]; !ok {
				return fmt.Errorf("%w: member %q is not imported", ErrReference, ref.Member)
			}
		}
	}
	return nil
}

func addSymbol(symbols map[string]string, value, kind string) error {
	if !validSymbol(value) {
		return fmt.Errorf("%w: %s %q", ErrInvalidID, kind, value)
	}
	if previous, exists := symbols[value]; exists {
		return fmt.Errorf("%w: %q is both %s and %s", ErrDuplicate, value, previous, kind)
	}
	symbols[value] = kind
	return nil
}

func nodeCount(model Model) int {
	count := 1 + len(model.Imports) + len(model.Tables) + len(model.Queries) + len(model.Plans) + len(model.Migrations)
	for _, table := range model.Tables {
		count += len(table.Columns) + len(table.Constraints) + len(table.Indexes)
	}
	for _, query := range model.Queries {
		conflictNodes := 0
		if query.Conflict.Constraint != "" || query.Conflict.Action != "" || len(query.Conflict.UpdateColumns) != 0 {
			conflictNodes = 1
		}
		count += len(query.Joins)*4 + len(query.Parameters) + len(query.Assignments) + conflictNodes + len(query.Conflict.UpdateColumns) + len(query.Projection) + len(query.GroupBy) + len(query.Returning) + len(query.Results) + len(query.Ordering) + predicateCount(query.Predicate) + predicateCount(query.Having)
	}
	for _, plan := range model.Plans {
		count += len(plan.Statements)
	}
	for _, migration := range model.Migrations {
		count += len(migration.Predecessors) + len(migration.Operations)
	}
	return count
}

func predicateCount(predicate Predicate) int {
	count := 0
	for _, item := range predicate.Items {
		count += 1 + predicateCount(item)
	}
	if predicate.Kind != "" {
		count++
	}
	return count
}

func validOperation(value OperationKind) bool {
	return value == OperationSelect || value == OperationInsert || value == OperationUpdate || value == OperationDelete
}

func validScalar(value ScalarType) bool {
	switch value {
	case TypeBool, TypeBytes, TypeFloat64, TypeInt64, TypeString, TypeTimestamp, TypeUint64, TypeUUID:
		return true
	default:
		return false
	}
}

func validMemberID(value string) bool {
	name, version, ok := strings.Cut(value, "/v")
	if !ok || name == "" || version == "" || strings.HasPrefix(version, "0") {
		return false
	}
	if _, err := strconv.ParseUint(version, 10, 64); err != nil {
		return false
	}
	segments := strings.Split(name, ".")
	if len(segments) < 2 {
		return false
	}
	for _, segment := range segments {
		if !validSegment(segment) {
			return false
		}
	}
	return true
}

func validNamespace(value string) bool {
	if value == "" || len(value) > 63 || !utf8.ValidString(value) || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func validSymbol(value string) bool {
	if value == "" || len(value) > MaximumSymbolBytes || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, part := range strings.Split(value, "-") {
		if part == "" || (part[0] < 'a' || part[0] > 'z') && (part[0] < '0' || part[0] > '9') {
			return false
		}
		for _, character := range part[1:] {
			if (character < 'a' || character > 'z') && (character < '0' || character > '9') {
				return false
			}
		}
	}
	return true
}

func validSegment(value string) bool {
	if value == "" || value[0] < 'a' || value[0] > 'z' {
		return false
	}
	for _, character := range value[1:] {
		if character != '-' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func refKey(ref Ref) string { return ref.Member + "\x00" + ref.Symbol }
