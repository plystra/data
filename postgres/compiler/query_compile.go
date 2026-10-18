package compiler

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/plystra/data/declaration"
)

var ErrQueryCompilation = errors.New("PostgreSQL query compilation failed")

// QueryArtifact is one deterministic SQL file for a declared query. Values
// are emitted as safe literals or PostgreSQL positional parameters; authored
// SQL is never accepted as input.
type QueryArtifact struct {
	MemberID string
	Path     string
	SQL      string
	Digest   string
	Results  []declaration.ResultField
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
			tableMember, table, err := resolveSchemaTable(member, query.Table, byID)
			if err != nil {
				return nil, fmt.Errorf("%w: query %s table: %w", ErrQueryCompilation, query.ID, err)
			}
			sql, err := compileQuery(member, tableMember, table, query, byID)
			if err != nil {
				return nil, fmt.Errorf("%w: query %s: %w", ErrQueryCompilation, query.ID, err)
			}
			artifacts = append(artifacts, QueryArtifact{
				MemberID: member.MemberID,
				Path:     "queries/" + member.MemberID + "/" + query.ID + ".sql",
				SQL:      sql,
				Digest:   DigestSQL(sql),
				Results:  append([]declaration.ResultField(nil), query.Results...),
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

type queryParameter struct {
	index int
	typ   declaration.ScalarType
}

func compileQuery(owner, tableMember SchemaMember, table declaration.Table, query declaration.Query, byID map[string]SchemaMember) (string, error) {
	if err := validateQueryResults(owner, tableMember, table, query, byID); err != nil {
		return "", err
	}
	parameters := make(map[string]queryParameter, len(query.Parameters))
	for index, parameter := range query.Parameters {
		parameters[parameter.ID] = queryParameter{index: index + 1, typ: parameter.Type}
	}
	switch query.Operation {
	case declaration.OperationSelect:
		return compileSelect(owner, tableMember, table, query, parameters, byID)
	case declaration.OperationInsert:
		return compileInsert(owner, tableMember, table, query, parameters)
	case declaration.OperationUpdate:
		return compileUpdate(owner, tableMember, table, query, parameters)
	case declaration.OperationDelete:
		return compileDelete(owner, tableMember, table, query, parameters)
	default:
		return "", fmt.Errorf("unsupported operation %q", query.Operation)
	}
}

func queryHeader(member SchemaMember, query declaration.Query, operation string) string {
	return fmt.Sprintf("-- plystra.data.postgres-query/v1 %s#%s\n%s", member.MemberID, query.ID, operation)
}

func compileSelect(owner, tableMember SchemaMember, table declaration.Table, query declaration.Query, parameters map[string]queryParameter, byID map[string]SchemaMember) (string, error) {
	qualifier := ""
	joins := ""
	aliases := map[declaration.Ref]joinedTable{query.Table: {table: table}}
	var err error
	if len(query.Joins) != 0 {
		qualifier = quoteIdentifier("t0") + "."
		joins, aliases, err = joinSQL(owner, table, query, byID)
		if err != nil {
			return "", err
		}
	}
	columns, err := selectQueryColumns(query, aliases, query.Projection, qualifier)
	if err != nil {
		return "", err
	}
	var sql strings.Builder
	sql.WriteString(queryHeader(owner, query, "SELECT "+columns+" FROM "+quoteIdentifier(tableMember.Model.Namespace)+"."+quoteIdentifier(table.ID)))
	if joins != "" {
		sql.WriteString(" AS ")
		sql.WriteString(quoteIdentifier("t0"))
		sql.WriteString(joins)
	}
	if query.Predicate.Kind != "" {
		predicate, err := predicateSQL(query.Predicate, query.Table, aliases, parameters)
		if err != nil {
			return "", err
		}
		sql.WriteString(" WHERE ")
		sql.WriteString(predicate)
	}
	if len(query.GroupBy) != 0 {
		grouping := make([]string, len(query.GroupBy))
		for index, item := range query.GroupBy {
			groupingTable := item.Table
			if groupingTable.Member == "" && groupingTable.Symbol == "" {
				groupingTable = query.Table
			}
			introduced, exists := aliases[groupingTable]
			if !exists {
				return "", fmt.Errorf("group by table %s is not introduced", groupingTable.Symbol)
			}
			if !hasColumn(introduced.table, item.Column) {
				return "", fmt.Errorf("group by column %s is not present", item.Column)
			}
			grouping[index] = columnSQL(introduced.alias, item.Column)
		}
		sql.WriteString(" GROUP BY ")
		sql.WriteString(strings.Join(grouping, ", "))
	}
	if query.Having.Kind != "" {
		having, err := havingSQL(query.Having, query, aliases, parameters)
		if err != nil {
			return "", err
		}
		sql.WriteString(" HAVING ")
		sql.WriteString(having)
	}
	if len(query.Ordering) != 0 {
		ordering := make([]string, len(query.Ordering))
		for index, item := range query.Ordering {
			if len(query.GroupBy) != 0 {
				if item.Table != (declaration.Ref{}) {
					return "", fmt.Errorf("grouped ordering cannot name a table")
				}
				found := false
				for _, projection := range query.Projection {
					name := projection.Column
					if projection.Alias != "" {
						name = projection.Alias
					}
					if name != item.Column {
						continue
					}
					expression, err := queryProjectionExpression(query, aliases, projection)
					if err != nil {
						return "", err
					}
					ordering[index] = expression.sql
					if projection.Aggregate == "" {
						ordering[index] = columnSQL(table.ID, projection.Column)
					}
					found = true
					break
				}
				if !found {
					return "", fmt.Errorf("grouped ordering output %s is not projected", item.Column)
				}
				if item.Direction == declaration.OrderingDescending {
					ordering[index] += " DESC"
				} else {
					ordering[index] += " ASC"
				}
				continue
			}
			orderingTable := query.Table
			if item.Table.Member != "" || item.Table.Symbol != "" {
				orderingTable = item.Table
			}
			introduced, exists := aliases[orderingTable]
			if !exists {
				return "", fmt.Errorf("ordering table %s is not introduced", orderingTable.Symbol)
			}
			if !hasColumn(introduced.table, item.Column) {
				return "", fmt.Errorf("ordering column %s is not present", item.Column)
			}
			direction := "ASC"
			if item.Direction == declaration.OrderingDescending {
				direction = "DESC"
			}
			ordering[index] = columnSQL(introduced.alias, item.Column) + " " + direction
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

type joinedTable struct {
	alias string
	table declaration.Table
}

func joinSQL(owner SchemaMember, baseTable declaration.Table, query declaration.Query, byID map[string]SchemaMember) (string, map[declaration.Ref]joinedTable, error) {
	introduced := map[declaration.Ref]joinedTable{query.Table: {alias: "t0", table: baseTable}}
	var sql strings.Builder
	for index, join := range query.Joins {
		member, table, err := resolveSchemaTable(owner, join.Table, byID)
		if err != nil {
			return "", nil, fmt.Errorf("join table %s: %w", join.Table.Symbol, err)
		}
		if _, exists := introduced[join.Table]; exists {
			return "", nil, fmt.Errorf("duplicate join table %s", join.Table.Symbol)
		}
		left, exists := introduced[join.On.Left.Table]
		if !exists {
			return "", nil, fmt.Errorf("join left table %s is not introduced", join.On.Left.Table.Symbol)
		}
		if join.On.Right.Table != join.Table {
			return "", nil, fmt.Errorf("join right table does not match %s", join.Table.Symbol)
		}
		leftColumn, exists := columnByID(left.table, join.On.Left.Column)
		if !exists {
			return "", nil, fmt.Errorf("join left column %s is not present", join.On.Left.Column)
		}
		rightColumn, exists := columnByID(table, join.On.Right.Column)
		if !exists {
			return "", nil, fmt.Errorf("join right column %s is not present", join.On.Right.Column)
		}
		if leftColumn.Type != rightColumn.Type {
			return "", nil, fmt.Errorf("join column types do not match: %s and %s", leftColumn.ID, rightColumn.ID)
		}
		kind := "INNER"
		if join.Kind == declaration.JoinLeft {
			kind = "LEFT"
		} else if join.Kind != declaration.JoinInner {
			return "", nil, fmt.Errorf("unsupported join kind %q", join.Kind)
		}
		alias := "t" + strconv.Itoa(index+1)
		fmt.Fprintf(&sql, " %s JOIN %s.%s AS %s ON %s.%s = %s.%s", kind, quoteIdentifier(member.Model.Namespace), quoteIdentifier(table.ID), quoteIdentifier(alias), quoteIdentifier(left.alias), quoteIdentifier(leftColumn.ID), quoteIdentifier(alias), quoteIdentifier(rightColumn.ID))
		introduced[join.Table] = joinedTable{alias: alias, table: table}
	}
	return sql.String(), introduced, nil
}

func compileInsert(owner, tableMember SchemaMember, table declaration.Table, query declaration.Query, parameters map[string]queryParameter) (string, error) {
	var sql strings.Builder
	sql.WriteString(queryHeader(owner, query, "INSERT INTO "+quoteIdentifier(tableMember.Model.Namespace)+"."+quoteIdentifier(table.ID)))
	if len(query.Assignments) == 0 {
		sql.WriteString(" DEFAULT VALUES")
	} else {
		columns, values, err := assignmentSQL(table, query.Assignments, parameters)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&sql, " (%s) VALUES (%s)", columns, values)
	}
	if query.Conflict.Action != "" || query.Conflict.Constraint != "" || len(query.Conflict.UpdateColumns) != 0 {
		found := false
		for _, constraint := range table.Constraints {
			if constraint.ID == query.Conflict.Constraint && (constraint.Kind == declaration.ConstraintPrimaryKey || constraint.Kind == declaration.ConstraintUnique) {
				found = true
				break
			}
		}
		if !found {
			return "", fmt.Errorf("conflict constraint %q is not a primary or unique table constraint", query.Conflict.Constraint)
		}
		fmt.Fprintf(&sql, " ON CONFLICT ON CONSTRAINT %s", quoteIdentifier(query.Conflict.Constraint))
		switch query.Conflict.Action {
		case declaration.ConflictDoNothing:
			sql.WriteString(" DO NOTHING")
		case declaration.ConflictDoUpdate:
			updates := make([]string, len(query.Conflict.UpdateColumns))
			for index, column := range query.Conflict.UpdateColumns {
				if !hasColumn(table, column) {
					return "", fmt.Errorf("conflict update column %s is not present", column)
				}
				updates[index] = quoteIdentifier(column) + " = EXCLUDED." + quoteIdentifier(column)
			}
			sql.WriteString(" DO UPDATE SET ")
			sql.WriteString(strings.Join(updates, ", "))
		default:
			return "", fmt.Errorf("unsupported conflict action %q", query.Conflict.Action)
		}
	}
	returning, err := returningSQL(table, query.Returning)
	if err != nil {
		return "", err
	}
	sql.WriteString(returning)
	sql.WriteString(";\n")
	return sql.String(), nil
}

func compileUpdate(owner, tableMember SchemaMember, table declaration.Table, query declaration.Query, parameters map[string]queryParameter) (string, error) {
	if query.Predicate.Kind == "" {
		return "", fmt.Errorf("update query requires a predicate")
	}
	values := make([]string, len(query.Assignments))
	for index, assignment := range query.Assignments {
		value, err := queryValueSQL(table, assignment.Column, assignment.Value, parameters)
		if err != nil {
			return "", err
		}
		values[index] = quoteIdentifier(assignment.Column) + " = " + value
	}
	predicate, err := predicateSQL(query.Predicate, query.Table, map[declaration.Ref]joinedTable{query.Table: {table: table}}, parameters)
	if err != nil {
		return "", err
	}
	returning, err := returningSQL(table, query.Returning)
	if err != nil {
		return "", err
	}
	return queryHeader(owner, query, "UPDATE "+quoteIdentifier(tableMember.Model.Namespace)+"."+quoteIdentifier(table.ID)+" SET "+strings.Join(values, ", ")+" WHERE "+predicate+returning+";\n"), nil
}

func compileDelete(owner, tableMember SchemaMember, table declaration.Table, query declaration.Query, parameters map[string]queryParameter) (string, error) {
	if query.Predicate.Kind == "" {
		return "", fmt.Errorf("delete query requires a predicate")
	}
	predicate, err := predicateSQL(query.Predicate, query.Table, map[declaration.Ref]joinedTable{query.Table: {table: table}}, parameters)
	if err != nil {
		return "", err
	}
	returning, err := returningSQL(table, query.Returning)
	if err != nil {
		return "", err
	}
	return queryHeader(owner, query, "DELETE FROM "+quoteIdentifier(tableMember.Model.Namespace)+"."+quoteIdentifier(table.ID)+" WHERE "+predicate+returning+";\n"), nil
}

func returningSQL(table declaration.Table, projection []declaration.Projection) (string, error) {
	if len(projection) == 0 {
		return "", nil
	}
	columns, err := selectColumns(table, projection)
	if err != nil {
		return "", err
	}
	return " RETURNING " + columns, nil
}

func validateQueryResults(owner, tableMember SchemaMember, table declaration.Table, query declaration.Query, byID map[string]SchemaMember) error {
	projection := query.Projection
	if query.Operation != declaration.OperationSelect {
		projection = query.Returning
	}
	if len(query.Results) != len(projection) {
		return fmt.Errorf("result mapping must cover each explicit output")
	}
	if len(projection) == 0 {
		return nil
	}
	outputs := make(map[string]declaration.Column, len(projection))
	for _, item := range projection {
		outputTable, err := resolveProjectionTable(owner, tableMember, table, query, item.Table, byID)
		if err != nil {
			return err
		}
		column := declaration.Column{ID: item.Column, Type: itemResultType(item)}
		if item.Aggregate == "" {
			var exists bool
			column, exists = columnByID(outputTable, item.Column)
			if !exists {
				return fmt.Errorf("result column %s is not present", item.Column)
			}
		} else if item.Aggregate == declaration.AggregateMin || item.Aggregate == declaration.AggregateMax {
			input, exists := columnByID(outputTable, item.Column)
			if !exists {
				return fmt.Errorf("%s aggregate column %s is not present", item.Aggregate, item.Column)
			}
			if input.Type != declaration.TypeInt64 {
				return fmt.Errorf("%s aggregate column %s is not int64", item.Aggregate, item.Column)
			}
		} else if item.Aggregate != declaration.AggregateCount {
			return fmt.Errorf("unsupported aggregate %q", item.Aggregate)
		}
		if (item.Aggregate == declaration.AggregateMin || item.Aggregate == declaration.AggregateMax) || item.Aggregate == "" && projectionCanBeNull(query, item.Table) {
			column.Nullable = true
		}
		name := item.Column
		if item.Alias != "" {
			name = item.Alias
		}
		if _, duplicate := outputs[name]; duplicate {
			return fmt.Errorf("duplicate result source %s", name)
		}
		outputs[name] = column
	}
	seen := make(map[string]struct{}, len(query.Results))
	for _, result := range query.Results {
		column, exists := outputs[result.Source]
		if !exists || column.Type != result.Type || column.Nullable != result.Nullable {
			return fmt.Errorf("result %s does not match source %s", result.ID, result.Source)
		}
		if _, duplicate := seen[result.Source]; duplicate {
			return fmt.Errorf("duplicate result source %s", result.Source)
		}
		seen[result.Source] = struct{}{}
	}
	return nil
}

func itemResultType(item declaration.Projection) declaration.ScalarType {
	if item.Aggregate == declaration.AggregateCount || item.Aggregate == declaration.AggregateMin || item.Aggregate == declaration.AggregateMax {
		return declaration.TypeInt64
	}
	return ""
}

func projectionCanBeNull(query declaration.Query, ref declaration.Ref) bool {
	if ref.Member == "" && ref.Symbol == "" {
		return false
	}
	for _, join := range query.Joins {
		if join.Kind == declaration.JoinLeft && join.Table == ref {
			return true
		}
	}
	return false
}

func resolveProjectionTable(owner, tableMember SchemaMember, table declaration.Table, query declaration.Query, ref declaration.Ref, byID map[string]SchemaMember) (declaration.Table, error) {
	if ref.Member == "" && ref.Symbol == "" {
		return table, nil
	}
	introduced := map[declaration.Ref]struct{}{query.Table: {}}
	for _, join := range query.Joins {
		introduced[join.Table] = struct{}{}
	}
	if _, exists := introduced[ref]; !exists {
		return declaration.Table{}, fmt.Errorf("projection table %s is not introduced", ref.Symbol)
	}
	_, target, err := resolveSchemaTable(owner, ref, byID)
	if err != nil {
		return declaration.Table{}, fmt.Errorf("projection table %s: %w", ref.Symbol, err)
	}
	return target, nil
}

func selectColumns(table declaration.Table, projection []declaration.Projection) (string, error) {
	return selectColumnsQualified(table, projection, "")
}

func selectColumnsQualified(table declaration.Table, projection []declaration.Projection, qualifier string) (string, error) {
	if len(projection) == 0 {
		return qualifier + "*", nil
	}
	columns := make([]string, len(projection))
	for index, item := range projection {
		if item.Aggregate != "" {
			return "", fmt.Errorf("aggregate projection is not valid for mutation output")
		}
		if !hasColumn(table, item.Column) {
			return "", fmt.Errorf("projection column %s is not present", item.Column)
		}
		columns[index] = qualifier + quoteIdentifier(item.Column)
		if item.Alias != "" {
			columns[index] += " AS " + quoteIdentifier(item.Alias)
		}
	}
	return strings.Join(columns, ", "), nil
}

type havingExpression struct {
	sql    string
	column declaration.Column
}

func selectQueryColumns(query declaration.Query, aliases map[declaration.Ref]joinedTable, projection []declaration.Projection, baseQualifier string) (string, error) {
	if len(projection) == 0 {
		return baseQualifier + "*", nil
	}
	columns := make([]string, len(projection))
	for index, item := range projection {
		expression, err := queryProjectionExpression(query, aliases, item)
		if err != nil {
			return "", err
		}
		columns[index] = expression.sql
		if item.Alias != "" {
			columns[index] += " AS " + quoteIdentifier(item.Alias)
		}
	}
	return strings.Join(columns, ", "), nil
}

func queryProjectionExpression(query declaration.Query, aliases map[declaration.Ref]joinedTable, item declaration.Projection) (havingExpression, error) {
	tableRef := query.Table
	if item.Table.Member != "" || item.Table.Symbol != "" {
		tableRef = item.Table
	}
	introduced, exists := aliases[tableRef]
	if !exists {
		return havingExpression{}, fmt.Errorf("projection table %s is not introduced", tableRef.Symbol)
	}
	if item.Aggregate != declaration.AggregateCount && !hasColumn(introduced.table, item.Column) {
		return havingExpression{}, fmt.Errorf("projection column %s is not present", item.Column)
	}
	if item.Aggregate == declaration.AggregateCount && item.Column != "" && !hasColumn(introduced.table, item.Column) {
		return havingExpression{}, fmt.Errorf("projection column %s is not present", item.Column)
	}
	if item.Aggregate != "" && item.Aggregate != declaration.AggregateCount && item.Aggregate != declaration.AggregateMin && item.Aggregate != declaration.AggregateMax {
		return havingExpression{}, fmt.Errorf("unsupported aggregate %q", item.Aggregate)
	}
	if item.Aggregate == declaration.AggregateMin || item.Aggregate == declaration.AggregateMax {
		column, ok := columnByID(introduced.table, item.Column)
		if !ok || column.Type != declaration.TypeInt64 {
			return havingExpression{}, fmt.Errorf("%s aggregate column %s is not int64", item.Aggregate, item.Column)
		}
	}
	if item.Aggregate == declaration.AggregateCount {
		argument := "*"
		if item.Column != "" {
			argument = columnSQL(introduced.alias, item.Column)
		}
		return havingExpression{sql: "COUNT(" + argument + ")", column: declaration.Column{ID: item.Column, Type: declaration.TypeInt64}}, nil
	}
	if item.Aggregate == declaration.AggregateMin || item.Aggregate == declaration.AggregateMax {
		function := "MIN"
		if item.Aggregate == declaration.AggregateMax {
			function = "MAX"
		}
		return havingExpression{sql: function + "(" + columnSQL(introduced.alias, item.Column) + ")", column: declaration.Column{ID: item.Column, Type: declaration.TypeInt64, Nullable: true}}, nil
	}
	column, _ := columnByID(introduced.table, item.Column)
	return havingExpression{sql: columnSQL(introduced.alias, item.Column), column: column}, nil
}

func havingSQL(predicate declaration.Predicate, query declaration.Query, aliases map[declaration.Ref]joinedTable, parameters map[string]queryParameter) (string, error) {
	outputs := make(map[string]havingExpression, len(query.Projection))
	for _, item := range query.Projection {
		expression, err := queryProjectionExpression(query, aliases, item)
		if err != nil {
			return "", err
		}
		name := item.Column
		if item.Alias != "" {
			name = item.Alias
		}
		outputs[name] = expression
	}
	return havingPredicateSQL(predicate, outputs, parameters)
}

func havingPredicateSQL(predicate declaration.Predicate, outputs map[string]havingExpression, parameters map[string]queryParameter) (string, error) {
	switch predicate.Kind {
	case declaration.PredicateEqual, declaration.PredicateNotEqual, declaration.PredicateLess, declaration.PredicateLessEqual, declaration.PredicateGreater, declaration.PredicateGreaterEqual:
		left, exists := outputs[predicate.Left.Column]
		if !exists {
			return "", fmt.Errorf("having output %s is not present", predicate.Left.Column)
		}
		var right string
		if predicateColumnSet(predicate.Right) {
			rightOutput, rightExists := outputs[predicate.Right.Column]
			if !rightExists {
				return "", fmt.Errorf("having output %s is not present", predicate.Right.Column)
			}
			if left.column.Type != rightOutput.column.Type {
				return "", fmt.Errorf("having output types do not match: %s and %s", predicate.Left.Column, predicate.Right.Column)
			}
			right = rightOutput.sql
		} else {
			var err error
			right, err = queryValueTypeSQL(left.column.Type, predicate.Left.Column, predicate.Value, parameters)
			if err != nil {
				return "", err
			}
		}
		operator := map[declaration.PredicateKind]string{
			declaration.PredicateEqual:        "=",
			declaration.PredicateNotEqual:     "<>",
			declaration.PredicateLess:         "<",
			declaration.PredicateLessEqual:    "<=",
			declaration.PredicateGreater:      ">",
			declaration.PredicateGreaterEqual: ">=",
		}[predicate.Kind]
		return left.sql + " " + operator + " " + right, nil
	case declaration.PredicateIsNull, declaration.PredicateIsNotNull:
		left, exists := outputs[predicate.Left.Column]
		if !exists {
			return "", fmt.Errorf("having output %s is not present", predicate.Left.Column)
		}
		suffix := " IS NULL"
		if predicate.Kind == declaration.PredicateIsNotNull {
			suffix = " IS NOT NULL"
		}
		return left.sql + suffix, nil
	case declaration.PredicateAnd, declaration.PredicateOr:
		parts := make([]string, len(predicate.Items))
		joiner := " AND "
		if predicate.Kind == declaration.PredicateOr {
			joiner = " OR "
		}
		for index, item := range predicate.Items {
			part, err := havingPredicateSQL(item, outputs, parameters)
			if err != nil {
				return "", err
			}
			parts[index] = "(" + part + ")"
		}
		return strings.Join(parts, joiner), nil
	default:
		return "", fmt.Errorf("unsupported having predicate %q", predicate.Kind)
	}
}

func columnSQL(alias, column string) string {
	if alias == "" {
		return quoteIdentifier(column)
	}
	return quoteIdentifier(alias) + "." + quoteIdentifier(column)
}

func predicateSQL(predicate declaration.Predicate, base declaration.Ref, aliases map[declaration.Ref]joinedTable, parameters map[string]queryParameter) (string, error) {
	switch predicate.Kind {
	case declaration.PredicateEqual, declaration.PredicateNotEqual,
		declaration.PredicateLess, declaration.PredicateLessEqual,
		declaration.PredicateGreater, declaration.PredicateGreaterEqual:
		left, column, err := resolvePredicateColumn(predicate.Left, base, aliases)
		if err != nil {
			return "", err
		}
		var right string
		if predicateColumnSet(predicate.Right) {
			rightTable, rightColumn, rightErr := resolvePredicateColumn(predicate.Right, base, aliases)
			if rightErr != nil {
				return "", rightErr
			}
			if column.Type != rightColumn.Type {
				return "", fmt.Errorf("predicate column types do not match: %s and %s", column.ID, rightColumn.ID)
			}
			right = columnSQL(rightTable.alias, rightColumn.ID)
		} else {
			if !predicate.Value.Set {
				return "", fmt.Errorf("predicate value for %s is not set", column.ID)
			}
			var valueErr error
			right, valueErr = queryValueSQL(left.table, column.ID, predicate.Value, parameters)
			if valueErr != nil {
				return "", valueErr
			}
		}
		operator := map[declaration.PredicateKind]string{
			declaration.PredicateEqual:        "=",
			declaration.PredicateNotEqual:     "<>",
			declaration.PredicateLess:         "<",
			declaration.PredicateLessEqual:    "<=",
			declaration.PredicateGreater:      ">",
			declaration.PredicateGreaterEqual: ">=",
		}[predicate.Kind]
		return columnSQL(left.alias, column.ID) + " " + operator + " " + right, nil
	case declaration.PredicateIsNull, declaration.PredicateIsNotNull:
		left, column, err := resolvePredicateColumn(predicate.Left, base, aliases)
		if err != nil {
			return "", err
		}
		suffix := " IS NULL"
		if predicate.Kind == declaration.PredicateIsNotNull {
			suffix = " IS NOT NULL"
		}
		return columnSQL(left.alias, column.ID) + suffix, nil
	case declaration.PredicateAnd, declaration.PredicateOr:
		parts := make([]string, len(predicate.Items))
		joiner := " AND "
		if predicate.Kind == declaration.PredicateOr {
			joiner = " OR "
		}
		for index, item := range predicate.Items {
			part, err := predicateSQL(item, base, aliases, parameters)
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

func predicateColumnSet(ref declaration.ColumnRef) bool {
	return ref.Table.Member != "" || ref.Table.Symbol != "" || ref.Column != ""
}

func resolvePredicateColumn(ref declaration.ColumnRef, base declaration.Ref, aliases map[declaration.Ref]joinedTable) (joinedTable, declaration.Column, error) {
	if ref.Table.Member == "" && ref.Table.Symbol == "" {
		ref.Table = base
	}
	table, exists := aliases[ref.Table]
	if !exists {
		return joinedTable{}, declaration.Column{}, fmt.Errorf("predicate table %s is not introduced", ref.Table.Symbol)
	}
	column, exists := columnByID(table.table, ref.Column)
	if !exists {
		return joinedTable{}, declaration.Column{}, fmt.Errorf("predicate column %s is not present", ref.Column)
	}
	return table, column, nil
}

func assignmentSQL(table declaration.Table, assignments []declaration.Assignment, parameters map[string]queryParameter) (string, string, error) {
	columns := make([]string, len(assignments))
	values := make([]string, len(assignments))
	for index, assignment := range assignments {
		if !hasColumn(table, assignment.Column) {
			return "", "", fmt.Errorf("assignment column %s is not present", assignment.Column)
		}
		columns[index] = quoteIdentifier(assignment.Column)
		value, err := queryValueSQL(table, assignment.Column, assignment.Value, parameters)
		if err != nil {
			return "", "", err
		}
		values[index] = value
	}
	return strings.Join(columns, ", "), strings.Join(values, ", "), nil
}

func queryValueSQL(table declaration.Table, column string, value declaration.Value, parameters map[string]queryParameter) (string, error) {
	definition, exists := columnByID(table, column)
	if !exists {
		return "", fmt.Errorf("column %s is not present", column)
	}
	return queryValueTypeSQL(definition.Type, column, value, parameters)
}

func queryValueTypeSQL(columnType declaration.ScalarType, column string, value declaration.Value, parameters map[string]queryParameter) (string, error) {
	if value.Parameter != "" {
		parameter, exists := parameters[value.Parameter]
		if !exists || parameter.typ != value.Kind || parameter.typ != columnType {
			return "", fmt.Errorf("parameter %s does not match column %s", value.Parameter, column)
		}
		return "$" + strconv.Itoa(parameter.index), nil
	}
	return defaultSQL(columnType, value)
}

func columnByID(table declaration.Table, id string) (declaration.Column, bool) {
	for _, column := range table.Columns {
		if column.ID == id {
			return column, true
		}
	}
	return declaration.Column{}, false
}
