package postgres

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/plystra/data/declaration"
)

var (
	ErrSchemaCompilation      = errors.New("PostgreSQL schema compilation failed")
	ErrCrossResourceReference = errors.New("cross-Resource Data reference")
)

// SchemaMember is one normalized Data model assigned to a selected database
// Resource. The compiler uses Resource only to reject cross-Resource foreign
// keys; it does not open or configure a provider.
type SchemaMember struct {
	MemberID string
	Resource string
	Model    declaration.Model
}

// SchemaArtifact is the deterministic PostgreSQL DDL for one frozen Resource
// instance. Query and access-code emission are separate compiler phases.
type SchemaArtifact struct {
	Path   string
	SQL    string
	Digest string
}

// CompileSchema lowers normalized relational schema intent into PostgreSQL
// DDL. Tables are created before foreign keys so valid cyclic table references
// can be represented without relying on input order.
func CompileSchema(members []SchemaMember) (SchemaArtifact, error) {
	if len(members) == 0 {
		return SchemaArtifact{}, fmt.Errorf("%w: no members", ErrSchemaCompilation)
	}
	normalized := make([]SchemaMember, len(members))
	byID := make(map[string]SchemaMember, len(members))
	namespaces := make(map[string]string, len(members))
	for index, member := range members {
		if strings.TrimSpace(member.MemberID) == "" || strings.TrimSpace(member.Resource) == "" {
			return SchemaArtifact{}, fmt.Errorf("%w: member %d identity or Resource", ErrSchemaCompilation, index)
		}
		model, err := (declaration.Member[struct{}]{
			Namespace:  member.Model.Namespace,
			Imports:    member.Model.Imports,
			Tables:     member.Model.Tables,
			Queries:    member.Model.Queries,
			Plans:      member.Model.Plans,
			Migrations: member.Model.Migrations,
		}).Normalize()
		if err != nil {
			return SchemaArtifact{}, fmt.Errorf("%w: member %s: %v", ErrSchemaCompilation, member.MemberID, err)
		}
		if _, exists := byID[member.MemberID]; exists {
			return SchemaArtifact{}, fmt.Errorf("%w: duplicate member %s", ErrSchemaCompilation, member.MemberID)
		}
		if previous, exists := namespaces[model.Namespace]; exists {
			return SchemaArtifact{}, fmt.Errorf("%w: namespace %s used by %s and %s", ErrSchemaCompilation, model.Namespace, previous, member.MemberID)
		}
		member.Model = model
		normalized[index] = member
		byID[member.MemberID] = member
		namespaces[model.Namespace] = member.MemberID
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Model.Namespace != normalized[j].Model.Namespace {
			return normalized[i].Model.Namespace < normalized[j].Model.Namespace
		}
		return normalized[i].MemberID < normalized[j].MemberID
	})

	var sql strings.Builder
	sql.WriteString("-- plystra.data.postgres-schema/v1\n")
	for _, member := range normalized {
		fmt.Fprintf(&sql, "CREATE SCHEMA IF NOT EXISTS %s;\n", quoteIdentifier(member.Model.Namespace))
	}
	sql.WriteByte('\n')
	for _, member := range normalized {
		for _, table := range member.Model.Tables {
			if err := writeTable(&sql, member, table); err != nil {
				return SchemaArtifact{}, err
			}
		}
	}
	for _, member := range normalized {
		for _, table := range member.Model.Tables {
			for _, constraint := range table.Constraints {
				if constraint.Kind != declaration.ConstraintForeignKey {
					continue
				}
				if err := writeForeignKey(&sql, member, table, constraint, byID); err != nil {
					return SchemaArtifact{}, err
				}
			}
			for _, index := range table.Indexes {
				writeIndex(&sql, member, table, index)
			}
		}
	}
	compiled := sql.String()
	return SchemaArtifact{Path: "schema/schema.sql", SQL: compiled, Digest: DigestSQL(compiled)}, nil
}

func writeTable(sql *strings.Builder, member SchemaMember, table declaration.Table) error {
	items := make([]string, 0, len(table.Columns)+len(table.Constraints))
	for _, column := range table.Columns {
		columnType, err := postgresType(column.Type)
		if err != nil {
			return fmt.Errorf("%w: table %s column %s: %v", ErrSchemaCompilation, table.ID, column.ID, err)
		}
		item := quoteIdentifier(column.ID) + " " + columnType
		if !column.Nullable {
			item += " NOT NULL"
		}
		if column.Default.Set {
			value, err := defaultSQL(column.Type, column.Default)
			if err != nil {
				return fmt.Errorf("%w: table %s column %s default: %v", ErrSchemaCompilation, table.ID, column.ID, err)
			}
			item += " DEFAULT " + value
		}
		items = append(items, item)
	}
	for _, constraint := range table.Constraints {
		if constraint.Kind == declaration.ConstraintForeignKey {
			continue
		}
		kind, err := constraintSQLKind(constraint.Kind)
		if err != nil {
			return fmt.Errorf("%w: table %s constraint %s: %v", ErrSchemaCompilation, table.ID, constraint.ID, err)
		}
		items = append(items, fmt.Sprintf("CONSTRAINT %s %s (%s)", quoteIdentifier(constraint.ID), kind, quoteIdentifiers(constraint.Columns)))
	}
	if len(items) == 0 {
		return fmt.Errorf("%w: table %s has no columns or constraints", ErrSchemaCompilation, table.ID)
	}
	fmt.Fprintf(sql, "CREATE TABLE %s.%s (\n    %s\n);\n\n", quoteIdentifier(member.Model.Namespace), quoteIdentifier(table.ID), strings.Join(items, ",\n    "))
	return nil
}

func writeForeignKey(sql *strings.Builder, member SchemaMember, table declaration.Table, constraint declaration.Constraint, byID map[string]SchemaMember) error {
	targetMember := member
	if constraint.ReferencedTable.Member != "" {
		var exists bool
		targetMember, exists = byID[constraint.ReferencedTable.Member]
		if !exists {
			return fmt.Errorf("%w: foreign key %s target member %s", ErrSchemaCompilation, constraint.ID, constraint.ReferencedTable.Member)
		}
		if targetMember.Resource != member.Resource {
			return fmt.Errorf("%w: %s to %s", ErrCrossResourceReference, member.MemberID, targetMember.MemberID)
		}
	}
	var targetTable declaration.Table
	found := false
	for _, candidate := range targetMember.Model.Tables {
		if candidate.ID == constraint.ReferencedTable.Symbol {
			targetTable = candidate
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("%w: foreign key %s target table %s", ErrSchemaCompilation, constraint.ID, constraint.ReferencedTable.Symbol)
	}
	if len(constraint.Columns) != len(constraint.ReferencedCols) {
		return fmt.Errorf("%w: foreign key %s column count", ErrSchemaCompilation, constraint.ID)
	}
	for _, column := range constraint.ReferencedCols {
		if !hasColumn(targetTable, column) {
			return fmt.Errorf("%w: foreign key %s target column %s", ErrSchemaCompilation, constraint.ID, column)
		}
	}
	fmt.Fprintf(sql, "ALTER TABLE %s.%s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s.%s (%s);\n", quoteIdentifier(member.Model.Namespace), quoteIdentifier(table.ID), quoteIdentifier(constraint.ID), quoteIdentifiers(constraint.Columns), quoteIdentifier(targetMember.Model.Namespace), quoteIdentifier(targetTable.ID), quoteIdentifiers(constraint.ReferencedCols))
	return nil
}

func writeIndex(sql *strings.Builder, member SchemaMember, table declaration.Table, index declaration.Index) {
	unique := ""
	if index.Unique {
		unique = " UNIQUE"
	}
	fmt.Fprintf(sql, "CREATE%s INDEX %s ON %s.%s (%s);\n", unique, quoteIdentifier(index.ID), quoteIdentifier(member.Model.Namespace), quoteIdentifier(table.ID), quoteIdentifiers(index.Columns))
}

func hasColumn(table declaration.Table, wanted string) bool {
	for _, column := range table.Columns {
		if column.ID == wanted {
			return true
		}
	}
	return false
}

func postgresType(value declaration.ScalarType) (string, error) {
	switch value {
	case declaration.TypeBool:
		return "BOOLEAN", nil
	case declaration.TypeBytes:
		return "BYTEA", nil
	case declaration.TypeFloat64:
		return "DOUBLE PRECISION", nil
	case declaration.TypeInt64:
		return "BIGINT", nil
	case declaration.TypeString:
		return "TEXT", nil
	case declaration.TypeTimestamp:
		return "TIMESTAMPTZ", nil
	case declaration.TypeUint64:
		return "NUMERIC(20,0)", nil
	case declaration.TypeUUID:
		return "UUID", nil
	default:
		return "", fmt.Errorf("unsupported scalar type %q", value)
	}
}

func constraintSQLKind(value declaration.ConstraintKind) (string, error) {
	switch value {
	case declaration.ConstraintPrimaryKey:
		return "PRIMARY KEY", nil
	case declaration.ConstraintUnique:
		return "UNIQUE", nil
	default:
		return "", fmt.Errorf("unsupported constraint kind %q", value)
	}
}

func defaultSQL(columnType declaration.ScalarType, value declaration.Value) (string, error) {
	if value.Kind != columnType {
		return "", fmt.Errorf("value kind %q does not match column type %q", value.Kind, columnType)
	}
	switch value.Kind {
	case declaration.TypeBool:
		if value.Bool {
			return "TRUE", nil
		}
		return "FALSE", nil
	case declaration.TypeBytes:
		decoded, err := hex.DecodeString(value.Bytes)
		if err != nil {
			return "", fmt.Errorf("bytes default is not hexadecimal")
		}
		return "decode('" + hex.EncodeToString(decoded) + "', 'hex')", nil
	case declaration.TypeFloat64:
		if math.IsNaN(value.Float64) || math.IsInf(value.Float64, 0) {
			return "", fmt.Errorf("non-finite floating-point default")
		}
		return strconv.FormatFloat(value.Float64, 'g', -1, 64), nil
	case declaration.TypeInt64:
		return strconv.FormatInt(value.Int64, 10), nil
	case declaration.TypeString:
		return "'" + strings.ReplaceAll(value.String, "'", "''") + "'", nil
	case declaration.TypeUint64:
		return strconv.FormatUint(value.Uint64, 10), nil
	default:
		return "", fmt.Errorf("defaults for scalar type %q are unsupported", value.Kind)
	}
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func quoteIdentifiers(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = quoteIdentifier(value)
	}
	return strings.Join(quoted, ", ")
}
