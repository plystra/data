package analyzer

import (
	"fmt"
	"go/types"
	"sort"

	"github.com/plystra/data/declaration"
)

func validateQueryMethodShapes(access types.Type, model declaration.Model) ([]QueryMethod, error) {
	queries := make(map[string]declaration.Query)
	for _, query := range model.Queries {
		if query.Method != "" {
			queries[query.Method] = query
		}
	}
	for _, plan := range model.Plans {
		if plan.Method != "" {
			return nil, fmt.Errorf("access plan method %q is not yet executable", plan.Method)
		}
	}
	iface := types.Unalias(access).(*types.Named).Underlying().(*types.Interface).Complete()
	if iface.NumMethods() == 0 {
		return nil, fmt.Errorf("access Resource has no callable query methods")
	}
	methods := make([]QueryMethod, 0, iface.NumMethods())
	for index := 0; index < iface.NumMethods(); index++ {
		method := iface.Method(index)
		query, ok := queries[method.Name()]
		if !ok {
			return nil, fmt.Errorf("access method %q has no supported query", method.Name())
		}
		fact, err := validateQueryMethodShape(method, query)
		if err != nil {
			return nil, fmt.Errorf("access method %q: %w", method.Name(), err)
		}
		methods = append(methods, fact)
	}
	sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })
	return methods, nil
}

func validateQueryMethodShape(method *types.Func, query declaration.Query) (QueryMethod, error) {
	if query.Operation != declaration.OperationSelect && len(query.Returning) == 0 {
		return QueryMethod{}, fmt.Errorf("direct mutation methods require RETURNING")
	}
	signature, ok := method.Type().(*types.Signature)
	if !ok || signature.Variadic() || signature.Params().Len() != 2 || signature.Results().Len() != 2 ||
		!isContextType(signature.Params().At(0).Type()) ||
		!types.Identical(types.Unalias(signature.Results().At(1).Type()), types.Universe.Lookup("error").Type()) {
		return QueryMethod{}, fmt.Errorf("expected (context.Context, Request) (Result, error) shape")
	}
	request, err := exportedStruct(signature.Params().At(1).Type())
	if err != nil {
		return QueryMethod{}, fmt.Errorf("request: %w", err)
	}
	parameters := make(map[string]accessField, len(query.Parameters))
	for _, parameter := range query.Parameters {
		parameters[parameter.GoField] = accessField{kind: parameter.Type}
	}
	if err := matchAccessFields(request, parameters); err != nil {
		return QueryMethod{}, fmt.Errorf("request: %w", err)
	}
	result := types.Unalias(signature.Results().At(0).Type())
	var rowType types.Type
	switch query.Cardinality {
	case declaration.CardinalityExactlyOne:
		rowType = result
	case declaration.CardinalityZeroOrOne:
		pointer, ok := result.(*types.Pointer)
		if !ok {
			return QueryMethod{}, fmt.Errorf("zero-or-one query must return *Row")
		}
		rowType = pointer.Elem()
	case declaration.CardinalityMany:
		slice, ok := result.(*types.Slice)
		if !ok {
			return QueryMethod{}, fmt.Errorf("many query must return []Row")
		}
		rowType = slice.Elem()
	default:
		return QueryMethod{}, fmt.Errorf("unsupported query cardinality")
	}
	row, err := exportedStruct(rowType)
	if err != nil {
		return QueryMethod{}, fmt.Errorf("row: %w", err)
	}
	results := make(map[string]accessField, len(query.Results))
	for _, result := range query.Results {
		results[result.GoField] = accessField{kind: result.Type, nullable: result.Nullable}
	}
	if err := matchAccessFields(row, results); err != nil {
		return QueryMethod{}, fmt.Errorf("row: %w", err)
	}
	return QueryMethod{
		Name: method.Name(), Cardinality: query.Cardinality,
		RequestPackage: request.Obj().Pkg().Path(), RequestType: request.Obj().Name(),
		RowPackage: row.Obj().Pkg().Path(), RowType: row.Obj().Name(),
	}, nil
}

type accessField struct {
	kind     declaration.ScalarType
	nullable bool
}

func matchAccessFields(named *types.Named, expected map[string]accessField) error {
	fields := named.Underlying().(*types.Struct)
	if fields.NumFields() != len(expected) {
		return fmt.Errorf("field count does not match declaration")
	}
	for index := 0; index < fields.NumFields(); index++ {
		field := fields.Field(index)
		shape, ok := expected[field.Name()]
		if !field.Exported() || field.Embedded() || !ok {
			return fmt.Errorf("field %q has no explicit mapping", field.Name())
		}
		want, ok := scalarGoType(shape.kind)
		if !ok {
			return fmt.Errorf("field %q has unsupported scalar type", field.Name())
		}
		if shape.nullable {
			want = types.NewPointer(want)
		}
		if !types.Identical(types.Unalias(field.Type()), want) {
			return fmt.Errorf("field %q type or nullability does not match declaration", field.Name())
		}
	}
	return nil
}

func exportedStruct(value types.Type) (*types.Named, error) {
	named, ok := types.Unalias(value).(*types.Named)
	if !ok || named.Obj().Pkg() == nil || !named.Obj().Exported() || named.TypeArgs().Len() != 0 {
		return nil, fmt.Errorf("expected an exported named struct")
	}
	if _, ok := named.Underlying().(*types.Struct); !ok {
		return nil, fmt.Errorf("expected an exported named struct")
	}
	return named, nil
}

func isContextType(value types.Type) bool {
	named, ok := types.Unalias(value).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == "context" && named.Obj().Name() == "Context"
}

func scalarGoType(kind declaration.ScalarType) (types.Type, bool) {
	switch kind {
	case declaration.TypeBool:
		return types.Typ[types.Bool], true
	case declaration.TypeBytes:
		return types.NewSlice(types.Typ[types.Uint8]), true
	case declaration.TypeFloat64:
		return types.Typ[types.Float64], true
	case declaration.TypeInt64:
		return types.Typ[types.Int64], true
	case declaration.TypeString:
		return types.Typ[types.String], true
	default:
		return nil, false
	}
}
