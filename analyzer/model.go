package analyzer

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/types"

	"github.com/plystra/data/declaration"
)

type modelDecoder struct {
	info *types.Info
}

func decodeModel(info *types.Info, root *ast.CompositeLit) (declaration.Model, error) {
	decoder := modelDecoder{info: info}
	fields, err := decoder.fields(root)
	if err != nil {
		return declaration.Model{}, err
	}
	model := declaration.Model{}
	if value, ok := fields["Namespace"]; ok {
		model.Namespace, err = decoder.string(value)
		if err != nil {
			return declaration.Model{}, fmt.Errorf("namespace: %w", err)
		}
	}
	if value, ok := fields["Imports"]; ok {
		model.Imports, err = decoder.stringSlice(value)
		if err != nil {
			return declaration.Model{}, fmt.Errorf("imports: %w", err)
		}
	}
	if value, ok := fields["Tables"]; ok {
		model.Tables, err = decoder.tables(value)
		if err != nil {
			return declaration.Model{}, fmt.Errorf("tables: %w", err)
		}
	}
	if value, ok := fields["Queries"]; ok {
		model.Queries, err = decoder.queries(value)
		if err != nil {
			return declaration.Model{}, fmt.Errorf("queries: %w", err)
		}
	}
	if value, ok := fields["Plans"]; ok {
		model.Plans, err = decoder.plans(value)
		if err != nil {
			return declaration.Model{}, fmt.Errorf("plans: %w", err)
		}
	}
	if value, ok := fields["Migrations"]; ok {
		model.Migrations, err = decoder.migrations(value)
		if err != nil {
			return declaration.Model{}, fmt.Errorf("migrations: %w", err)
		}
	}
	normalized, err := (declaration.Member[struct{}]{
		Namespace:  model.Namespace,
		Imports:    model.Imports,
		Tables:     model.Tables,
		Queries:    model.Queries,
		Plans:      model.Plans,
		Migrations: model.Migrations,
	}).Normalize()
	if err != nil {
		return declaration.Model{}, err
	}
	return normalized, nil
}

func (d modelDecoder) fields(literal *ast.CompositeLit) (map[string]ast.Expr, error) {
	fields := make(map[string]ast.Expr, len(literal.Elts))
	for _, element := range literal.Elts {
		field, ok := element.(*ast.KeyValueExpr)
		if !ok {
			return nil, fmt.Errorf("struct literal is not keyed")
		}
		key, ok := field.Key.(*ast.Ident)
		if !ok {
			return nil, fmt.Errorf("struct field key is not an identifier")
		}
		fields[key.Name] = field.Value
	}
	return fields, nil
}

func (d modelDecoder) slice(value ast.Expr) ([]ast.Expr, error) {
	literal, ok := value.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("expected a slice literal")
	}
	return literal.Elts, nil
}

func (d modelDecoder) fieldsExpr(value ast.Expr) (map[string]ast.Expr, error) {
	literal, ok := value.(*ast.CompositeLit)
	if !ok {
		return nil, fmt.Errorf("expected a struct literal")
	}
	return d.fields(literal)
}

func (d modelDecoder) string(value ast.Expr) (string, error) {
	constantValue, err := d.constant(value)
	if err != nil || constantValue.Kind() != constant.String {
		return "", fmt.Errorf("expected a string constant")
	}
	return constant.StringVal(constantValue), nil
}

func (d modelDecoder) stringSlice(value ast.Expr) ([]string, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]string, len(elements))
	for index, element := range elements {
		result[index], err = d.string(element)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", index, err)
		}
	}
	return result, nil
}

func (d modelDecoder) bool(value ast.Expr) (bool, error) {
	constantValue, err := d.constant(value)
	if err != nil || constantValue.Kind() != constant.Bool {
		return false, fmt.Errorf("expected a Boolean constant")
	}
	return constant.BoolVal(constantValue), nil
}

func (d modelDecoder) uint64(value ast.Expr) (uint64, error) {
	constantValue, err := d.constant(value)
	if err != nil || constantValue.Kind() != constant.Int {
		return 0, fmt.Errorf("expected an unsigned integer constant")
	}
	result, exact := constant.Uint64Val(constantValue)
	if !exact {
		return 0, fmt.Errorf("integer constant is out of range")
	}
	return result, nil
}

func (d modelDecoder) int64(value ast.Expr) (int64, error) {
	constantValue, err := d.constant(value)
	if err != nil || constantValue.Kind() != constant.Int {
		return 0, fmt.Errorf("expected a signed integer constant")
	}
	result, exact := constant.Int64Val(constantValue)
	if !exact {
		return 0, fmt.Errorf("integer constant is out of range")
	}
	return result, nil
}

func (d modelDecoder) float64(value ast.Expr) (float64, error) {
	constantValue, err := d.constant(value)
	if err != nil || (constantValue.Kind() != constant.Float && constantValue.Kind() != constant.Int) {
		return 0, fmt.Errorf("expected a floating-point constant")
	}
	result, exact := constant.Float64Val(constantValue)
	if !exact {
		return 0, fmt.Errorf("floating-point constant is out of range")
	}
	return result, nil
}

func (d modelDecoder) scalar(value ast.Expr) (declaration.ScalarType, error) {
	result, err := d.string(value)
	return declaration.ScalarType(result), err
}

func (d modelDecoder) ref(value ast.Expr) (declaration.Ref, error) {
	fields, err := d.fieldsExpr(value)
	if err != nil {
		return declaration.Ref{}, err
	}
	result := declaration.Ref{}
	if field, ok := fields["Member"]; ok {
		result.Member, err = d.string(field)
		if err != nil {
			return declaration.Ref{}, fmt.Errorf("member: %w", err)
		}
	}
	if field, ok := fields["Symbol"]; ok {
		result.Symbol, err = d.string(field)
		if err != nil {
			return declaration.Ref{}, fmt.Errorf("symbol: %w", err)
		}
	}
	return result, nil
}

func (d modelDecoder) value(value ast.Expr) (declaration.Value, error) {
	fields, err := d.fieldsExpr(value)
	if err != nil {
		return declaration.Value{}, err
	}
	result := declaration.Value{}
	if field, ok := fields["Kind"]; ok {
		result.Kind, err = d.scalar(field)
		if err != nil {
			return declaration.Value{}, fmt.Errorf("kind: %w", err)
		}
	}
	if field, ok := fields["Set"]; ok {
		result.Set, err = d.bool(field)
		if err != nil {
			return declaration.Value{}, fmt.Errorf("set: %w", err)
		}
	}
	if field, ok := fields["String"]; ok {
		result.String, err = d.string(field)
		if err != nil {
			return declaration.Value{}, fmt.Errorf("string: %w", err)
		}
	}
	if field, ok := fields["Int64"]; ok {
		result.Int64, err = d.int64(field)
		if err != nil {
			return declaration.Value{}, fmt.Errorf("int64: %w", err)
		}
	}
	if field, ok := fields["Uint64"]; ok {
		result.Uint64, err = d.uint64(field)
		if err != nil {
			return declaration.Value{}, fmt.Errorf("uint64: %w", err)
		}
	}
	if field, ok := fields["Float64"]; ok {
		result.Float64, err = d.float64(field)
		if err != nil {
			return declaration.Value{}, fmt.Errorf("float64: %w", err)
		}
	}
	if field, ok := fields["Bool"]; ok {
		result.Bool, err = d.bool(field)
		if err != nil {
			return declaration.Value{}, fmt.Errorf("bool: %w", err)
		}
	}
	if field, ok := fields["Bytes"]; ok {
		result.Bytes, err = d.string(field)
		if err != nil {
			return declaration.Value{}, fmt.Errorf("bytes: %w", err)
		}
	}
	if field, ok := fields["Parameter"]; ok {
		result.Parameter, err = d.string(field)
		if err != nil {
			return declaration.Value{}, fmt.Errorf("parameter: %w", err)
		}
	}
	return result, nil
}

func (d modelDecoder) parameters(value ast.Expr) ([]declaration.Parameter, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Parameter, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		parameter := declaration.Parameter{}
		if field, ok := fields["ID"]; ok {
			parameter.ID, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Type"]; ok {
			parameter.Type, err = d.scalar(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = parameter
	}
	return result, nil
}

func (d modelDecoder) assignments(value ast.Expr) ([]declaration.Assignment, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Assignment, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		assignment := declaration.Assignment{}
		if field, ok := fields["Column"]; ok {
			assignment.Column, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Value"]; ok {
			assignment.Value, err = d.value(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = assignment
	}
	return result, nil
}

func (d modelDecoder) tables(value ast.Expr) ([]declaration.Table, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Table, len(elements))
	for index, element := range elements {
		result[index], err = d.table(element)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", index, err)
		}
	}
	return result, nil
}

func (d modelDecoder) table(value ast.Expr) (declaration.Table, error) {
	fields, err := d.fieldsExpr(value)
	if err != nil {
		return declaration.Table{}, err
	}
	result := declaration.Table{}
	if field, ok := fields["ID"]; ok {
		result.ID, err = d.string(field)
		if err != nil {
			return declaration.Table{}, err
		}
	}
	if field, ok := fields["Columns"]; ok {
		result.Columns, err = d.columns(field)
		if err != nil {
			return declaration.Table{}, fmt.Errorf("columns: %w", err)
		}
	}
	if field, ok := fields["Constraints"]; ok {
		result.Constraints, err = d.constraints(field)
		if err != nil {
			return declaration.Table{}, fmt.Errorf("constraints: %w", err)
		}
	}
	if field, ok := fields["Indexes"]; ok {
		result.Indexes, err = d.indexes(field)
		if err != nil {
			return declaration.Table{}, fmt.Errorf("indexes: %w", err)
		}
	}
	return result, nil
}

func (d modelDecoder) columns(value ast.Expr) ([]declaration.Column, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Column, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		column := declaration.Column{}
		if field, ok := fields["ID"]; ok {
			column.ID, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Type"]; ok {
			column.Type, err = d.scalar(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Nullable"]; ok {
			column.Nullable, err = d.bool(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Default"]; ok {
			column.Default, err = d.value(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = column
	}
	return result, nil
}

func (d modelDecoder) constraints(value ast.Expr) ([]declaration.Constraint, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Constraint, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		constraint := declaration.Constraint{}
		if field, ok := fields["ID"]; ok {
			constraint.ID, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Kind"]; ok {
			value, valueErr := d.string(field)
			if valueErr != nil {
				return nil, valueErr
			}
			constraint.Kind = declaration.ConstraintKind(value)
		}
		if field, ok := fields["Columns"]; ok {
			constraint.Columns, err = d.stringSlice(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["ReferencedTable"]; ok {
			constraint.ReferencedTable, err = d.ref(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["ReferencedCols"]; ok {
			constraint.ReferencedCols, err = d.stringSlice(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = constraint
	}
	return result, nil
}

func (d modelDecoder) indexes(value ast.Expr) ([]declaration.Index, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Index, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		item := declaration.Index{}
		if field, ok := fields["ID"]; ok {
			item.ID, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Columns"]; ok {
			item.Columns, err = d.stringSlice(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Unique"]; ok {
			item.Unique, err = d.bool(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = item
	}
	return result, nil
}

func (d modelDecoder) queries(value ast.Expr) ([]declaration.Query, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Query, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		query := declaration.Query{}
		if field, ok := fields["ID"]; ok {
			query.ID, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Operation"]; ok {
			value, valueErr := d.string(field)
			if valueErr != nil {
				return nil, valueErr
			}
			query.Operation = declaration.OperationKind(value)
		}
		if field, ok := fields["Table"]; ok {
			query.Table, err = d.ref(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Parameters"]; ok {
			query.Parameters, err = d.parameters(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Assignments"]; ok {
			query.Assignments, err = d.assignments(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Projection"]; ok {
			query.Projection, err = d.projections(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Predicate"]; ok {
			query.Predicate, err = d.predicate(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Ordering"]; ok {
			query.Ordering, err = d.ordering(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Limit"]; ok {
			query.Limit, err = d.uint64(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["HasLimit"]; ok {
			query.HasLimit, err = d.bool(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Offset"]; ok {
			query.Offset, err = d.uint64(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["HasOffset"]; ok {
			query.HasOffset, err = d.bool(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = query
	}
	return result, nil
}

func (d modelDecoder) projections(value ast.Expr) ([]declaration.Projection, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Projection, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		item := declaration.Projection{}
		if field, ok := fields["Column"]; ok {
			item.Column, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Alias"]; ok {
			item.Alias, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = item
	}
	return result, nil
}

func (d modelDecoder) predicate(value ast.Expr) (declaration.Predicate, error) {
	fields, err := d.fieldsExpr(value)
	if err != nil {
		return declaration.Predicate{}, err
	}
	result := declaration.Predicate{}
	if field, ok := fields["Kind"]; ok {
		kind, kindErr := d.string(field)
		if kindErr != nil {
			return declaration.Predicate{}, kindErr
		}
		result.Kind = declaration.PredicateKind(kind)
	}
	if field, ok := fields["Left"]; ok {
		result.Left, err = d.string(field)
		if err != nil {
			return declaration.Predicate{}, err
		}
	}
	if field, ok := fields["Right"]; ok {
		result.Right, err = d.string(field)
		if err != nil {
			return declaration.Predicate{}, err
		}
	}
	if field, ok := fields["Value"]; ok {
		result.Value, err = d.value(field)
		if err != nil {
			return declaration.Predicate{}, err
		}
	}
	if field, ok := fields["Items"]; ok {
		result.Items, err = d.predicates(field)
		if err != nil {
			return declaration.Predicate{}, err
		}
	}
	return result, nil
}

func (d modelDecoder) predicates(value ast.Expr) ([]declaration.Predicate, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Predicate, len(elements))
	for index, element := range elements {
		result[index], err = d.predicate(element)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", index, err)
		}
	}
	return result, nil
}

func (d modelDecoder) ordering(value ast.Expr) ([]declaration.Ordering, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Ordering, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		item := declaration.Ordering{}
		if field, ok := fields["Column"]; ok {
			item.Column, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Direction"]; ok {
			value, valueErr := d.string(field)
			if valueErr != nil {
				return nil, valueErr
			}
			item.Direction = declaration.OrderingDirection(value)
		}
		result[index] = item
	}
	return result, nil
}

func (d modelDecoder) plans(value ast.Expr) ([]declaration.Plan, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Plan, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		plan := declaration.Plan{}
		if field, ok := fields["ID"]; ok {
			plan.ID, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Statements"]; ok {
			plan.Statements, err = d.planStatements(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = plan
	}
	return result, nil
}

func (d modelDecoder) planStatements(value ast.Expr) ([]declaration.PlanStatement, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.PlanStatement, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		statement := declaration.PlanStatement{}
		if field, ok := fields["ID"]; ok {
			statement.ID, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Query"]; ok {
			statement.Query, err = d.ref(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Cardinality"]; ok {
			value, valueErr := d.string(field)
			if valueErr != nil {
				return nil, valueErr
			}
			statement.Cardinality = declaration.Cardinality(value)
		}
		if field, ok := fields["Affected"]; ok {
			statement.Affected, err = d.affected(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = statement
	}
	return result, nil
}

func (d modelDecoder) affected(value ast.Expr) (declaration.AffectedRows, error) {
	fields, err := d.fieldsExpr(value)
	if err != nil {
		return declaration.AffectedRows{}, err
	}
	result := declaration.AffectedRows{}
	if field, ok := fields["Set"]; ok {
		result.Set, err = d.bool(field)
		if err != nil {
			return declaration.AffectedRows{}, err
		}
	}
	if field, ok := fields["Min"]; ok {
		result.Min, err = d.uint64(field)
		if err != nil {
			return declaration.AffectedRows{}, err
		}
	}
	if field, ok := fields["Max"]; ok {
		result.Max, err = d.uint64(field)
		if err != nil {
			return declaration.AffectedRows{}, err
		}
	}
	if field, ok := fields["Unbounded"]; ok {
		result.Unbounded, err = d.bool(field)
		if err != nil {
			return declaration.AffectedRows{}, err
		}
	}
	return result, nil
}

func (d modelDecoder) migrations(value ast.Expr) ([]declaration.Migration, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Migration, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		migration := declaration.Migration{}
		if field, ok := fields["ID"]; ok {
			migration.ID, err = d.string(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Version"]; ok {
			migration.Version, err = d.uint64(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Predecessors"]; ok {
			migration.Predecessors, err = d.refs(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Operations"]; ok {
			migration.Operations, err = d.migrationOperations(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = migration
	}
	return result, nil
}

func (d modelDecoder) refs(value ast.Expr) ([]declaration.Ref, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.Ref, len(elements))
	for index, element := range elements {
		result[index], err = d.ref(element)
		if err != nil {
			return nil, fmt.Errorf("element %d: %w", index, err)
		}
	}
	return result, nil
}

func (d modelDecoder) migrationOperations(value ast.Expr) ([]declaration.MigrationOperation, error) {
	elements, err := d.slice(value)
	if err != nil {
		return nil, err
	}
	result := make([]declaration.MigrationOperation, len(elements))
	for index, element := range elements {
		fields, fieldErr := d.fieldsExpr(element)
		if fieldErr != nil {
			return nil, fieldErr
		}
		operation := declaration.MigrationOperation{}
		if field, ok := fields["Kind"]; ok {
			value, valueErr := d.string(field)
			if valueErr != nil {
				return nil, valueErr
			}
			operation.Kind = declaration.MigrationOperationKind(value)
		}
		if field, ok := fields["Table"]; ok {
			operation.Table, err = d.ref(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Column"]; ok {
			operation.Column, err = d.column(field)
			if err != nil {
				return nil, err
			}
		}
		if field, ok := fields["Index"]; ok {
			operation.Index, err = d.index(field)
			if err != nil {
				return nil, err
			}
		}
		result[index] = operation
	}
	return result, nil
}

func (d modelDecoder) index(value ast.Expr) (declaration.Index, error) {
	fields, err := d.fieldsExpr(value)
	if err != nil {
		return declaration.Index{}, err
	}
	result := declaration.Index{}
	if field, ok := fields["ID"]; ok {
		result.ID, err = d.string(field)
		if err != nil {
			return declaration.Index{}, err
		}
	}
	if field, ok := fields["Columns"]; ok {
		result.Columns, err = d.stringSlice(field)
		if err != nil {
			return declaration.Index{}, err
		}
	}
	if field, ok := fields["Unique"]; ok {
		result.Unique, err = d.bool(field)
		if err != nil {
			return declaration.Index{}, err
		}
	}
	return result, nil
}

func (d modelDecoder) column(value ast.Expr) (declaration.Column, error) {
	columns, err := d.columns(&ast.CompositeLit{Elts: []ast.Expr{value}})
	if err != nil || len(columns) != 1 {
		return declaration.Column{}, err
	}
	return columns[0], nil
}

func (d modelDecoder) constant(value ast.Expr) (constant.Value, error) {
	typed, ok := d.info.Types[value]
	if !ok || typed.Value == nil {
		return nil, fmt.Errorf("expression is not a compile-time constant")
	}
	return typed.Value, nil
}
