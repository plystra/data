package compiler

import (
	"fmt"
	"strings"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/database"
	"github.com/plystra/data/declaration"
)

func emitPlanMethod(source *strings.Builder, method analyzer.PlanMethod, artifact PlanArtifact, queries []declaration.Query, packages map[string]string) error {
	resultType := packages[method.ResultPackage] + "." + method.ResultType
	fmt.Fprintf(source, "\nfunc (provider *Provider) %s(ctx context.Context, request %s.%s) (%s, error) {\nvar zero %s\nif provider == nil || provider.database == nil { return zero, db.ErrUnavailable }\nresult, err := provider.database.Exec(ctx, db.Plan{Statements: []db.Statement{", method.Name, packages[method.RequestPackage], method.RequestType, resultType, resultType)
	for _, statement := range artifact.Plan.Statements {
		fmt.Fprintf(source, "{ID: %q, SQL: %q, ReturnsRows: %t, Cardinality: db.Cardinality(%q), Affected: db.AffectedRows{Set: %t, Min: %d, Max: %d, Unbounded: %t}, Args: []db.Argument{", statement.ID, statement.SQL, statement.ReturnsRows, statement.Cardinality, statement.Affected.Set, statement.Affected.Min, statement.Affected.Max, statement.Affected.Unbounded)
		for _, argument := range statement.Args {
			if argument.FromInput != nil {
				fmt.Fprintf(source, "db.InputArgument(db.ValueKind(%q), %q),", argument.Kind, argument.FromInput.ID)
			} else if argument.FromResult != nil {
				fmt.Fprintf(source, "db.ResultArgument(db.ValueKind(%q), %q, %d),", argument.Kind, argument.FromResult.Statement, argument.FromResult.Column)
			} else {
				return fmt.Errorf("unsupported compiled plan argument")
			}
		}
		source.WriteString("}, ResultColumns: []db.ResultColumn{")
		for _, column := range statement.ResultColumns {
			fmt.Fprintf(source, "{Kind: db.ValueKind(%q), Nullable: %t},", column.Kind, column.Nullable)
		}
		source.WriteString("}},")
	}
	source.WriteString("}, Inputs: map[string]db.Argument{")
	for _, parameter := range artifact.Definition.Parameters {
		fmt.Fprintf(source, "%q: db.%sArgument(request.%s),", parameter.ID, accessValueField(parameter.Type), parameter.GoField)
	}
	source.WriteString("}})\nif err != nil { return zero, err }\n")
	fmt.Fprintf(source, "if len(result.Statements) != %d { return zero, db.ErrBackend }\n", len(artifact.Plan.Statements))
	for index, statement := range artifact.Plan.Statements {
		emitPlanResultValidation(source, index, statement)
	}
	fmt.Fprintf(source, "var output %s\n", resultType)
	for outputIndex, output := range artifact.Definition.Outputs {
		for statementIndex, statement := range artifact.Definition.Statements {
			if statement.ID != output.Statement {
				continue
			}
			query, ok := findQuery(queries, statement.Query.Symbol)
			if !ok {
				return fmt.Errorf("missing plan output query")
			}
			column, err := planResultColumn(query, output.Result)
			if err != nil {
				return err
			}
			for _, field := range query.Results {
				if field.ID != output.Result {
					continue
				}
				cell := fmt.Sprintf("result.Statements[%d].Rows[0][%d]", statementIndex, column)
				value := cell + "." + accessValueField(field.Type)
				if field.Type == declaration.TypeBytes {
					value = "append([]byte{}, " + value + "...)"
				}
				if field.Nullable {
					fmt.Fprintf(source, "if %s.Kind != db.ValueNull { value%d := %s; output.%s = &value%d }\n", cell, outputIndex, value, output.GoField, outputIndex)
				} else {
					fmt.Fprintf(source, "output.%s = %s\n", output.GoField, value)
				}
			}
		}
	}
	source.WriteString("return output, nil\n}\n")
	return nil
}

func emitPlanResultValidation(source *strings.Builder, index int, statement database.Statement) {
	fmt.Fprintf(source, "{ statement := result.Statements[%d]\n", index)
	if !statement.ReturnsRows {
		source.WriteString("if len(statement.Rows) != 0 { return zero, db.ErrBackend }\n")
	} else {
		switch statement.Cardinality {
		case database.CardinalityExactlyOne:
			source.WriteString("if len(statement.Rows) == 0 { return zero, db.ErrNotFound }; if len(statement.Rows) != 1 { return zero, db.ErrBackend }\n")
		case database.CardinalityZeroOrOne:
			source.WriteString("if len(statement.Rows) > 1 { return zero, db.ErrBackend }\n")
		}
		fmt.Fprintf(source, "for _, cells := range statement.Rows {\nif len(cells) != %d { return zero, db.ErrBackend }\n", len(statement.ResultColumns))
		for columnIndex, column := range statement.ResultColumns {
			fmt.Fprintf(source, "if cells[%d].Kind != db.ValueKind(%q)", columnIndex, column.Kind)
			if column.Nullable {
				fmt.Fprintf(source, " && cells[%d].Kind != db.ValueNull", columnIndex)
			}
			source.WriteString(" { return zero, db.ErrBackend }\n")
		}
		source.WriteString("}\n")
	}
	if statement.Affected.Set {
		fmt.Fprintf(source, "if statement.AffectedRows < %d", statement.Affected.Min)
		if !statement.Affected.Unbounded {
			fmt.Fprintf(source, " || statement.AffectedRows > %d", statement.Affected.Max)
		}
		source.WriteString(" { return zero, db.ErrBackend }\n")
	} else {
		source.WriteString("if statement.AffectedRows != 0 { return zero, db.ErrBackend }\n")
	}
	source.WriteString("}\n")
}
