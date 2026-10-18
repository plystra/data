package analyzer

import (
	"sort"

	"github.com/plystra/data/declaration"
)

type memberSymbols struct {
	tables     map[string]declaration.Table
	queries    map[string]struct{}
	plans      map[string]struct{}
	migrations map[string]struct{}
}

func (member memberSymbols) hasSymbol(symbol string) bool {
	if _, ok := member.tables[symbol]; ok {
		return true
	}
	if _, ok := member.queries[symbol]; ok {
		return true
	}
	if _, ok := member.plans[symbol]; ok {
		return true
	}
	_, ok := member.migrations[symbol]
	return ok
}

func (a *analysis) validateReferences() {
	roots := append([]Root(nil), a.result.Roots...)
	sort.Slice(roots, func(i, j int) bool { return roots[i].MemberID < roots[j].MemberID })

	symbols := make(map[string]memberSymbols, len(roots))
	for _, root := range roots {
		member := memberSymbols{
			tables:     make(map[string]declaration.Table, len(root.Model.Tables)),
			queries:    make(map[string]struct{}, len(root.Model.Queries)),
			plans:      make(map[string]struct{}, len(root.Model.Plans)),
			migrations: make(map[string]struct{}, len(root.Model.Migrations)),
		}
		for _, table := range root.Model.Tables {
			member.tables[table.ID] = table
		}
		for _, query := range root.Model.Queries {
			member.queries[query.ID] = struct{}{}
		}
		for _, plan := range root.Model.Plans {
			member.plans[plan.ID] = struct{}{}
		}
		for _, migration := range root.Model.Migrations {
			member.migrations[migration.ID] = struct{}{}
		}
		symbols[root.MemberID] = member
	}

	imports := make(map[string][]string, len(roots))
	for _, root := range roots {
		for _, memberID := range root.Model.Imports {
			if _, exists := symbols[memberID]; !exists {
				a.referenceDiagnostic(root, CodeUnresolvedReference, "Data member import target is not present in the analyzed package inventory", "Add the imported Data member to the selected module graph or remove the import.", memberID)
				continue
			}
			imports[root.MemberID] = append(imports[root.MemberID], memberID)
		}
		a.validateMemberReferences(root, symbols)
	}
	a.validateImportCycles(roots, imports)
}

func (a *analysis) validateMemberReferences(root Root, symbols map[string]memberSymbols) {
	for _, table := range root.Model.Tables {
		for _, constraint := range table.Constraints {
			if constraint.Kind != declaration.ConstraintForeignKey {
				continue
			}
			target, ok := a.resolveTable(root, constraint.ReferencedTable, symbols)
			if ok {
				a.validateColumns(root, target, constraint.ReferencedCols)
			}
		}
	}
	for _, query := range root.Model.Queries {
		target, ok := a.resolveTable(root, query.Table, symbols)
		if !ok {
			continue
		}
		if query.Table.Member != "" && (query.Conflict.Constraint != "" || query.Conflict.Action != "" || len(query.Conflict.UpdateColumns) != 0) && !hasConflictConstraint(target, query.Conflict.Constraint) {
			a.referenceDiagnostic(root, CodeUnresolvedReference, "Data conflict target is not a primary-key or unique table constraint", "Name a primary-key or unique constraint on the explicitly referenced table.", query.Conflict.Constraint)
		}
		for _, column := range query.Conflict.UpdateColumns {
			a.validateColumn(root, target, column)
		}
		for _, assignment := range query.Assignments {
			column, exists := columnByID(target, assignment.Column)
			if !exists {
				a.validateColumn(root, target, assignment.Column)
				continue
			}
			if column.Type != assignment.Value.Kind {
				a.referenceDiagnostic(root, CodeTypeCheck, "Data assignment value does not match its target column", "Match the assignment value type to the selected table column.", assignment.Column)
			}
		}
		a.validateJoinReferences(root, query, target, symbols)
		introduced := map[declaration.Ref]declaration.Table{query.Table: target}
		for _, join := range query.Joins {
			if joined, joinedOK := a.resolveTable(root, join.Table, symbols); joinedOK {
				introduced[join.Table] = joined
			}
		}
		for _, grouping := range query.GroupBy {
			groupingTable := grouping.Table
			if groupingTable.Member == "" && groupingTable.Symbol == "" {
				groupingTable = query.Table
			}
			groupingTarget, exists := introduced[groupingTable]
			if !exists {
				a.referenceDiagnostic(root, CodeUnresolvedReference, "Data group-by table is not introduced by the query", "Group only by columns from the base table or an earlier join.", groupingTable.Symbol)
				continue
			}
			a.validateColumn(root, groupingTarget, grouping.Column)
		}
		projections := query.Projection
		if query.Operation != declaration.OperationSelect {
			projections = query.Returning
		}
		outputs := make(map[string]declaration.Column, len(projections))
		for _, projection := range projections {
			projectionTable := projection.Table
			if projectionTable.Member == "" && projectionTable.Symbol == "" {
				projectionTable = query.Table
			}
			projectionTarget, exists := introduced[projectionTable]
			if !exists {
				a.referenceDiagnostic(root, CodeUnresolvedReference, "Data projection table is not introduced by the query", "Project only columns from the base table or an earlier join.", projectionTable.Symbol)
				continue
			}
			if projection.Aggregate == declaration.AggregateCount && projection.Column == "" {
				continue
			}
			a.validateColumn(root, projectionTarget, projection.Column)
			column, exists := columnByID(projectionTarget, projection.Column)
			if !exists {
				continue
			}
			if projection.Aggregate == declaration.AggregateMin || projection.Aggregate == declaration.AggregateMax {
				if column.Type != declaration.TypeInt64 {
					a.referenceDiagnostic(root, CodeTypeCheck, "Data aggregate input is not int64", "Use an int64 column for MIN or MAX.", projection.Column)
				}
				continue
			}
			if projection.Aggregate != "" {
				continue
			}
			for _, join := range query.Joins {
				if join.Kind == declaration.JoinLeft && join.Table == projectionTable {
					column.Nullable = true
				}
			}
			name := projection.Column
			if projection.Alias != "" {
				name = projection.Alias
			}
			outputs[name] = column
		}
		for _, result := range query.Results {
			column, exists := outputs[result.Source]
			if exists && (result.Type != column.Type || result.Nullable != column.Nullable) {
				a.referenceDiagnostic(root, CodeTypeCheck, "Data query result does not match its projected column", "Match the result scalar type and nullability to the selected output, including LEFT JOIN outputs.", result.Source)
			}
		}
		for _, ordering := range query.Ordering {
			orderingTable := ordering.Table
			if orderingTable.Member == "" && orderingTable.Symbol == "" {
				orderingTable = query.Table
			}
			orderingTarget, exists := introduced[orderingTable]
			if !exists {
				a.referenceDiagnostic(root, CodeUnresolvedReference, "Data ordering table is not introduced by the query", "Order only by columns from the base table or an earlier join.", orderingTable.Symbol)
				continue
			}
			a.validateColumn(root, orderingTarget, ordering.Column)
		}
		a.validatePredicate(root, query.Table, query.Predicate, introduced)
	}
	for _, plan := range root.Model.Plans {
		for _, statement := range plan.Statements {
			a.resolveKind(root, statement.Query, "query", symbols)
		}
	}
	for _, migration := range root.Model.Migrations {
		for _, predecessor := range migration.Predecessors {
			a.resolveKind(root, predecessor, "migration", symbols)
		}
		for _, operation := range migration.Operations {
			a.resolveKind(root, operation.Table, "table", symbols)
		}
	}
}

func (a *analysis) validateJoinReferences(root Root, query declaration.Query, base declaration.Table, symbols map[string]memberSymbols) {
	introduced := map[declaration.Ref]declaration.Table{query.Table: base}
	for _, join := range query.Joins {
		target, ok := a.resolveTable(root, join.Table, symbols)
		if !ok {
			continue
		}
		leftTable, ok := introduced[join.On.Left.Table]
		if !ok {
			continue // A prior unresolved join was already reported.
		}
		left, leftOK := columnByID(leftTable, join.On.Left.Column)
		right, rightOK := columnByID(target, join.On.Right.Column)
		if !leftOK {
			a.validateColumn(root, leftTable, join.On.Left.Column)
		}
		if !rightOK {
			a.validateColumn(root, target, join.On.Right.Column)
		}
		if leftOK && rightOK && left.Type != right.Type {
			a.referenceDiagnostic(root, CodeTypeCheck, "Data join columns have different scalar types", "Compare columns with the same scalar type.", join.On.Right.Column)
		}
		introduced[join.Table] = target
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

func hasConflictConstraint(table declaration.Table, id string) bool {
	for _, constraint := range table.Constraints {
		if constraint.ID == id && (constraint.Kind == declaration.ConstraintPrimaryKey || constraint.Kind == declaration.ConstraintUnique) {
			return true
		}
	}
	return false
}

func (a *analysis) resolveTable(root Root, ref declaration.Ref, symbols map[string]memberSymbols) (declaration.Table, bool) {
	memberID := root.MemberID
	if ref.Member != "" {
		memberID = ref.Member
	}
	member, exists := symbols[memberID]
	if !exists {
		a.referenceDiagnostic(root, CodeUnresolvedReference, "Data reference target member is not present in the analyzed package inventory", "Add the referenced Data member to the selected module graph.", ref.Symbol)
		return declaration.Table{}, false
	}
	table, exists := member.tables[ref.Symbol]
	if !exists {
		code := CodeUnresolvedReference
		message := "Data reference symbol is not present in the target member"
		action := "Declare the referenced Data symbol or correct the reference."
		if member.hasSymbol(ref.Symbol) {
			code = CodeReferenceKindMismatch
			message = "Data reference does not name a table in the target member"
			action = "Use the symbol of a declared table for this reference."
		}
		a.referenceDiagnostic(root, code, message, action, ref.Symbol)
		return declaration.Table{}, false
	}
	return table, true
}

func (a *analysis) resolveKind(root Root, ref declaration.Ref, kind string, symbols map[string]memberSymbols) bool {
	memberID := root.MemberID
	if ref.Member != "" {
		memberID = ref.Member
	}
	member, exists := symbols[memberID]
	if !exists {
		a.referenceDiagnostic(root, CodeUnresolvedReference, "Data reference target member is not present in the analyzed package inventory", "Add the referenced Data member to the selected module graph.", ref.Symbol)
		return false
	}
	var found bool
	switch kind {
	case "query":
		_, found = member.queries[ref.Symbol]
	case "plan":
		_, found = member.plans[ref.Symbol]
	case "migration":
		_, found = member.migrations[ref.Symbol]
	case "table":
		_, found = member.tables[ref.Symbol]
	}
	if !found {
		code := CodeUnresolvedReference
		message := "Data reference symbol is not present in the target member"
		action := "Declare the referenced Data symbol or correct the reference."
		if member.hasSymbol(ref.Symbol) {
			code = CodeReferenceKindMismatch
			message = "Data reference does not name the required symbol kind in the target member"
			action = "Use a symbol declared by the required Data node type."
		}
		a.referenceDiagnostic(root, code, message, action, ref.Symbol)
	}
	return found
}

func (a *analysis) validateColumns(root Root, table declaration.Table, columns []string) {
	for _, column := range columns {
		a.validateColumn(root, table, column)
	}
}

func (a *analysis) validateColumn(root Root, table declaration.Table, column string) {
	for _, candidate := range table.Columns {
		if candidate.ID == column {
			return
		}
	}
	a.referenceDiagnostic(root, CodeUnresolvedReference, "Data reference does not name a column in the target table", "Use a declared column from the referenced table.", column)
}

func (a *analysis) validatePredicate(root Root, base declaration.Ref, predicate declaration.Predicate, introduced map[declaration.Ref]declaration.Table) {
	if predicate.Kind == "" {
		return
	}
	validate := func(ref declaration.ColumnRef) {
		if ref.Column == "" && ref.Table.Member == "" && ref.Table.Symbol == "" {
			return
		}
		if ref.Table.Member == "" && ref.Table.Symbol == "" {
			ref.Table = base
		}
		table, exists := introduced[ref.Table]
		if !exists {
			a.referenceDiagnostic(root, CodeUnresolvedReference, "Data predicate table is not introduced by the query", "Filter only columns from the base table or an earlier join.", ref.Table.Symbol)
			return
		}
		a.validateColumn(root, table, ref.Column)
	}
	validate(predicate.Left)
	validate(predicate.Right)
	for _, item := range predicate.Items {
		a.validatePredicate(root, base, item, introduced)
	}
}

func (a *analysis) validateImportCycles(roots []Root, imports map[string][]string) {
	state := make(map[string]uint8, len(roots))
	reported := make(map[string]struct{})
	stack := make([]string, 0, len(roots))
	var visit func(string)
	visit = func(memberID string) {
		state[memberID] = 1
		stack = append(stack, memberID)
		dependencies := append([]string(nil), imports[memberID]...)
		sort.Strings(dependencies)
		for _, dependency := range dependencies {
			switch state[dependency] {
			case 0:
				visit(dependency)
			case 1:
				start := 0
				for index, item := range stack {
					if item == dependency {
						start = index
						break
					}
				}
				for _, cycleMember := range stack[start:] {
					if _, exists := reported[cycleMember]; exists {
						continue
					}
					reported[cycleMember] = struct{}{}
					a.referenceDiagnosticByMember(cycleMember, CodeImportCycle, "Data member imports contain a cycle", "Remove one import edge so Data member imports form an acyclic graph.", cycleMember)
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[memberID] = 2
	}
	ids := make([]string, 0, len(roots))
	for _, root := range roots {
		ids = append(ids, root.MemberID)
	}
	sort.Strings(ids)
	for _, memberID := range ids {
		if state[memberID] == 0 {
			visit(memberID)
		}
	}
}

func (a *analysis) referenceDiagnostic(root Root, code DiagnosticCode, message, action, symbol string) {
	a.result.Diagnostics = append(a.result.Diagnostics, Diagnostic{
		Code:        code,
		Message:     message,
		Action:      action,
		PackagePath: root.PackagePath,
		FilePath:    root.FilePath,
		MemberID:    root.MemberID,
		Symbol:      symbol,
		Span:        root.DeclarationPos,
	})
}

func (a *analysis) referenceDiagnosticByMember(memberID string, code DiagnosticCode, message, action, symbol string) {
	for _, root := range a.result.Roots {
		if root.MemberID == memberID {
			a.referenceDiagnostic(root, code, message, action, symbol)
			return
		}
	}
}
