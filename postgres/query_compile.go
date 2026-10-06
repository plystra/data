package postgres

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/plystra/data/declaration"
)

var ErrQueryCompilation = errors.New("PostgreSQL query compilation failed")

// QueryArtifact is one deterministic SQL file for a declared query. The
// initial compiler emits read-only SELECT intent; mutation queries remain
// rejected until the logical model carries typed parameters and assignments.
type QueryArtifact struct {
	MemberID string
	Path     string
	SQL      string
	Digest   string
}

// CompileQueries lowers the currently supported query subset for members
// assigned to one Resource. Member and query order never affects the output.
func CompileQueries(members []SchemaMember) ([]QueryArtifact, error) {
	normalized, byID, err := normalizeQueryMembers(members)
	if err != nil {
		return nil, err
	}
	artifacts := make([]QueryArtifact, 0)
	for _, member := range normalized {
		for _, query := range member.Model.Queries {
			if query.Operation != declaration.OperationSelect {
				return nil, fmt.Errorf("%w: query %s operation %q requires typed mutation inputs", ErrQueryCompilation, query.ID, query.Operation)
			}
			tableMember, table, err := resolveSchemaTable(member, query.Table, byID)
			if err != nil {
				return nil, fmt.Errorf("%w: query %s table: %w", ErrQueryCompilation, query.ID, err)
			}
			sql, err := compileSelect(tableMember, table, query)
			if err != nil {
				return nil, fmt.Errorf("%w: query %s: %w", ErrQueryCompilation, query.ID, err)
			}
			artifacts = append(artifacts, QueryArtifact{
				MemberID: member.MemberID,
				Path:     "queries/" + member.MemberID + "/" + query.ID + ".sql",
				SQL:      sql,
				Digest:   DigestSQL(sql),
			})
		}
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts, nil
}

func normalizeQueryMembers(members []SchemaMember) ([]SchemaMember, map[string]SchemaMember, error) {
	if len(members) == 0 {
		return nil, nil, fmt.Errorf("%w: no members", ErrQueryCompilation)
	}
	normalized := make([]SchemaMember, len(members))
	byID := make(map[string]SchemaMember, len(members))
	namespaces := make(map[string]string, len(members))
	for index, member := range members {
		if strings.TrimSpace(member.MemberID) == "" || strings.TrimSpace(member.Resource) == "" {
			return nil, nil, fmt.Errorf("%w: member %d identity or Resource", ErrQueryCompilation, index)
		}
		model, err := (declaration.Member[struct{}]{
			Namespace: member.Model.Namespace, Imports: member.Model.Imports,
			Tables: member.Model.Tables, Queries: member.Model.Queries,
			Plans: member.Model.Plans, Migrations: member.Model.Migrations,
		}).Normalize()
		if err != nil {
			return nil, nil, fmt.Errorf("%w: member %s: %v", ErrQueryCompilation, member.MemberID, err)
		}
		if _, exists := byID[member.MemberID]; exists {
			return nil, nil, fmt.Errorf("%w: duplicate member %s", ErrQueryCompilation, member.MemberID)
		}
		if previous, exists := namespaces[model.Namespace]; exists {
			return nil, nil, fmt.Errorf("%w: namespace %s used by %s and %s", ErrQueryCompilation, model.Namespace, previous, member.MemberID)
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
	return normalized, byID, nil
}

func compileSelect(member SchemaMember, table declaration.Table, query declaration.Query) (string, error) {
	columns, err := selectColumns(table, query.Projection)
	if err != nil {
		return "", err
	}
	var sql strings.Builder
	fmt.Fprintf(&sql, "-- plystra.data.postgres-query/v1 %s#%s\nSELECT %s FROM %s.%s", member.MemberID, query.ID, columns, quoteIdentifier(member.Model.Namespace), quoteIdentifier(table.ID))
	if query.Predicate.Kind != "" {
		predicate, err := predicateSQL(query.Predicate, table)
		if err != nil {
			return "", err
		}
		sql.WriteString(" WHERE ")
		sql.WriteString(predicate)
	}
	if len(query.Ordering) != 0 {
		ordering := make([]string, len(query.Ordering))
		for index, item := range query.Ordering {
			if !hasColumn(table, item.Column) {
				return "", fmt.Errorf("ordering column %s is not present", item.Column)
			}
			direction := "ASC"
			if item.Direction == declaration.OrderingDescending {
				direction = "DESC"
			}
			ordering[index] = quoteIdentifier(item.Column) + " " + direction
		}
		sql.WriteString(" ORDER BY ")
		sql.WriteString(strings.Join(ordering, ", "))
	}
	if query.HasLimit {
		fmt.Fprintf(&sql, " LIMIT %s", strconv.FormatUint(query.Limit, 10))
	}
	if query.HasOffset {
		fmt.Fprintf(&sql, " OFFSET %s", strconv.FormatUint(query.Offset, 10))
	}
	sql.WriteString(";\n")
	return sql.String(), nil
}

func selectColumns(table declaration.Table, projection []declaration.Projection) (string, error) {
	if len(projection) == 0 {
		return "*", nil
	}
	columns := make([]string, len(projection))
	for index, item := range projection {
		if !hasColumn(table, item.Column) {
			return "", fmt.Errorf("projection column %s is not present", item.Column)
		}
		columns[index] = quoteIdentifier(item.Column)
		if item.Alias != "" {
			columns[index] += " AS " + quoteIdentifier(item.Alias)
		}
	}
	return strings.Join(columns, ", "), nil
}

func predicateSQL(predicate declaration.Predicate, table declaration.Table) (string, error) {
	switch predicate.Kind {
	case declaration.PredicateEqual, declaration.PredicateNotEqual,
		declaration.PredicateLess, declaration.PredicateLessEqual,
		declaration.PredicateGreater, declaration.PredicateGreaterEqual:
		column, exists := columnByID(table, predicate.Left)
		if !exists {
			return "", fmt.Errorf("predicate column %s is not present", predicate.Left)
		}
		if !predicate.Value.Set {
			return "", fmt.Errorf("predicate value for %s is not set", predicate.Left)
		}
		value, err := defaultSQL(column.Type, predicate.Value)
		if err != nil {
			return "", err
		}
		operator := map[declaration.PredicateKind]string{
			declaration.PredicateEqual:        "=",
			declaration.PredicateNotEqual:     "<>",
			declaration.PredicateLess:         "<",
			declaration.PredicateLessEqual:    "<=",
			declaration.PredicateGreater:      ">",
			declaration.PredicateGreaterEqual: ">=",
		}[predicate.Kind]
		return quoteIdentifier(column.ID) + " " + operator + " " + value, nil
	case declaration.PredicateIsNull:
		if !hasColumn(table, predicate.Left) {
			return "", fmt.Errorf("predicate column %s is not present", predicate.Left)
		}
		return quoteIdentifier(predicate.Left) + " IS NULL", nil
	case declaration.PredicateAnd, declaration.PredicateOr:
		parts := make([]string, len(predicate.Items))
		joiner := " AND "
		if predicate.Kind == declaration.PredicateOr {
			joiner = " OR "
		}
		for index, item := range predicate.Items {
			part, err := predicateSQL(item, table)
			if err != nil {
				return "", err
			}
			parts[index] = "(" + part + ")"
		}
		return strings.Join(parts, joiner), nil
	default:
		return "", fmt.Errorf("unsupported predicate %q", predicate.Kind)
	}
}

func columnByID(table declaration.Table, id string) (declaration.Column, bool) {
	for _, column := range table.Columns {
		if column.ID == id {
			return column, true
		}
	}
	return declaration.Column{}, false
}
