package compiler

import (
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"sort"
	"strconv"
	"strings"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
)

var ErrAccessCompilation = errors.New("PostgreSQL access provider compilation failed")

// AccessArtifact implements one complete authored access Resource. Source is
// ordinary Go; the provider borrows the selected database Resource and owns no
// pool or lifecycle hooks.
type AccessArtifact struct {
	MemberID string
	Path     string
	Source   string
	Digest   string
}

// CompileAccessProviders consumes accepted analyzer type identities alongside
// the assigned models. It supports callable queries and atomic plans;
// any unsupported callable method rejects the entire output.
func CompileAccessProviders(members []SchemaMember, roots []analyzer.Root) ([]AccessArtifact, error) {
	normalized, _, err := normalizeQueryMembers(members)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAccessCompilation, err)
	}
	queries, err := CompileQueries(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAccessCompilation, err)
	}
	byPath := make(map[string]QueryArtifact, len(queries))
	for _, query := range queries {
		byPath[query.Path] = query
	}
	plans, err := CompilePlans(normalized)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrAccessCompilation, err)
	}
	byPlan := make(map[string]PlanArtifact, len(plans))
	for _, plan := range plans {
		byPlan[plan.Path] = plan
	}
	byRoot := make(map[string]analyzer.Root, len(roots))
	for _, root := range roots {
		if _, exists := byRoot[root.MemberID]; exists {
			return nil, fmt.Errorf("%w: duplicate root", ErrAccessCompilation)
		}
		byRoot[root.MemberID] = root
	}
	artifacts := make([]AccessArtifact, 0)
	for _, member := range normalized {
		root, ok := byRoot[member.MemberID]
		if !ok || !declaration.ValidMemberID(member.MemberID) {
			return nil, fmt.Errorf("%w: missing accepted root", ErrAccessCompilation)
		}
		modelDigest, err := declaration.DigestModel(member.Model)
		if err != nil {
			return nil, fmt.Errorf("%w: invalid model", ErrAccessCompilation)
		}
		rootDigest, err := declaration.DigestModel(root.Model)
		if err != nil || rootDigest != modelDigest {
			return nil, fmt.Errorf("%w: changed accepted model", ErrAccessCompilation)
		}
		if err := validateAccessRoot(root); err != nil {
			return nil, fmt.Errorf("%w: member %s: %v", ErrAccessCompilation, member.MemberID, err)
		}
		if root.MigrationOnly {
			continue
		}
		source, err := compileAccessProvider(member, root, byPath, byPlan)
		if err != nil {
			return nil, fmt.Errorf("%w: member %s: %v", ErrAccessCompilation, member.MemberID, err)
		}
		artifacts = append(artifacts, AccessArtifact{MemberID: member.MemberID, Path: "providers/" + member.MemberID + "/provider.go", Source: source, Digest: DigestSQL(source)})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	return artifacts, nil
}

func validateAccessRoot(root analyzer.Root) error {
	queries := make(map[string]declaration.Query)
	for _, query := range root.Model.Queries {
		if query.Method != "" {
			queries[query.Method] = query
		}
	}
	plans := make(map[string]declaration.Plan)
	for _, plan := range root.Model.Plans {
		if plan.Method != "" {
			plans[plan.Method] = plan
		}
	}
	if root.MigrationOnly {
		if len(queries)+len(plans) != 0 || len(root.QueryMethods) != 0 || len(root.PlanMethods) != 0 || root.AccessID != "" || root.AccessType != "NoAccess" || root.AccessPackage != "github.com/plystra/data/declaration" {
			return fmt.Errorf("NoAccess member carries an access contract")
		}
		return nil
	}
	if !declaration.ValidMemberID(root.AccessID) || !validAccessName(root.AccessType) || !validAccessPackage(root.AccessPackage) || len(queries)+len(plans) == 0 || len(root.QueryMethods) != len(queries) || len(root.PlanMethods) != len(plans) {
		return fmt.Errorf("incomplete access contract")
	}
	if !portableAccessPackage(root.AccessPackage) {
		return fmt.Errorf("access contract requires a public package without internal or vendor segments")
	}
	seen := make(map[string]bool)
	for _, method := range root.QueryMethods {
		query, ok := queries[method.Name]
		if !ok || seen[method.Name] || !validAccessName(method.Name) || method.Cardinality != query.Cardinality ||
			!validAccessName(method.RequestType) || !validAccessPackage(method.RequestPackage) {
			return fmt.Errorf("invalid query method inventory")
		}
		if !portableAccessPackage(method.RequestPackage) {
			return fmt.Errorf("method %s requires a public request package without internal or vendor segments", method.Name)
		}
		seen[method.Name] = true
		if query.Operation != declaration.OperationSelect && len(query.Returning) == 0 {
			if query.Cardinality != declaration.CardinalityNone || method.RowType != "" || method.RowPackage != "" {
				return fmt.Errorf("method %s requires an affected-count contract", method.Name)
			}
		} else {
			if len(queryProjections(query)) == 0 || query.Cardinality == declaration.CardinalityNone ||
				!validAccessName(method.RowType) || !validAccessPackage(method.RowPackage) || !portableAccessPackage(method.RowPackage) {
				return fmt.Errorf("method %s requires projected rows in a public package without internal or vendor segments", method.Name)
			}
		}
		if _, err := planCardinality(query.Cardinality); err != nil {
			return err
		}
		if _, err := resultColumns(query); err != nil {
			return err
		}
		for _, parameter := range query.Parameters {
			if _, ok := databaseValueKind(parameter.Type); !ok || !validAccessName(parameter.GoField) {
				return fmt.Errorf("unsupported parameter codec")
			}
		}
		for _, result := range query.Results {
			if !validAccessName(result.GoField) {
				return fmt.Errorf("missing result field mapping")
			}
		}
	}
	for _, method := range root.PlanMethods {
		plan, ok := plans[method.Name]
		if !ok || seen[method.Name] || !validAccessName(method.Name) ||
			!validAccessName(method.RequestType) || !validAccessPackage(method.RequestPackage) || !validAccessName(method.ResultType) || !validAccessPackage(method.ResultPackage) {
			return fmt.Errorf("invalid plan method inventory")
		}
		if !portableAccessPackage(method.RequestPackage) || !portableAccessPackage(method.ResultPackage) {
			return fmt.Errorf("method %s requires public request and result packages without internal or vendor segments", method.Name)
		}
		seen[method.Name] = true
		for _, parameter := range plan.Parameters {
			if _, ok := databaseValueKind(parameter.Type); !ok || !validAccessName(parameter.GoField) {
				return fmt.Errorf("unsupported plan parameter codec")
			}
		}
	}
	return nil
}

func validAccessName(name string) bool { return token.IsIdentifier(name) && token.IsExported(name) }

func validAccessPackage(name string) bool {
	if name == "" || strings.ContainsAny(name, "\\\"`:\x00\r\n\t ") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func portableAccessPackage(name string) bool {
	// Emit has no consumer package identity. A path accessible to the authored
	// model may still be inaccessible to the downstream generated provider.
	for _, part := range strings.Split(name, "/") {
		if part == "internal" || part == "vendor" {
			return false
		}
	}
	return true
}

func compileAccessProvider(member SchemaMember, root analyzer.Root, byPath map[string]QueryArtifact, byPlan map[string]PlanArtifact) (string, error) {
	packages := map[string]string{root.AccessPackage: ""}
	for _, method := range root.QueryMethods {
		packages[method.RequestPackage] = ""
		if method.RowPackage != "" {
			packages[method.RowPackage] = ""
		}
	}
	for _, method := range root.PlanMethods {
		packages[method.RequestPackage] = ""
		packages[method.ResultPackage] = ""
	}
	paths := make([]string, 0, len(packages))
	for path := range packages {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var source strings.Builder
	source.WriteString("// Code generated by Plystra Data. DO NOT EDIT.\n\npackage access\n\nimport (\n\"context\"\ndb \"github.com/plystra/data/database\"\n")
	for index, path := range paths {
		alias := fmt.Sprintf("contract%d", index)
		// Reuse infrastructure imports if an authored type is from that package.
		if path == "context" {
			alias = "context"
		} else if path == "github.com/plystra/data/database" {
			alias = "db"
		} else {
			fmt.Fprintf(&source, "%s %q\n", alias, path)
		}
		packages[path] = alias
	}
	fmt.Fprintf(&source, ")\n\nvar _ %s.%s = (*Provider)(nil)\n\ntype Provider struct { database db.Resource }\n\n//plystra:implements-resource %s\nfunc New(database db.Resource) (*Provider, error) {\nif database == nil { return nil, db.ErrUnavailable }; return &Provider{database: database}, nil\n}\n", packages[root.AccessPackage], root.AccessType, root.AccessID)
	methods := append([]analyzer.QueryMethod(nil), root.QueryMethods...)
	sort.Slice(methods, func(i, j int) bool { return methods[i].Name < methods[j].Name })
	for _, method := range methods {
		var query declaration.Query
		for _, candidate := range member.Model.Queries {
			if candidate.Method == method.Name {
				query = candidate
				break
			}
		}
		artifact, ok := byPath["queries/"+member.MemberID+"/"+query.ID+".sql"]
		if !ok {
			return "", fmt.Errorf("missing compiled query")
		}
		emitAccessMethod(&source, method, query, artifact.SQL, packages)
	}
	planMethods := append([]analyzer.PlanMethod(nil), root.PlanMethods...)
	sort.Slice(planMethods, func(i, j int) bool { return planMethods[i].Name < planMethods[j].Name })
	for _, method := range planMethods {
		var selected declaration.Plan
		for _, plan := range member.Model.Plans {
			if plan.Method == method.Name {
				selected = plan
				break
			}
		}
		artifact, ok := byPlan["plans/"+member.MemberID+"/"+selected.ID+".json"]
		if !ok {
			return "", fmt.Errorf("missing compiled plan")
		}
		if err := emitPlanMethod(&source, method, artifact, member.Model.Queries, packages); err != nil {
			return "", err
		}
	}
	formatted, err := format.Source([]byte(source.String()))
	if err != nil {
		return "", fmt.Errorf("invalid generated source: %w", err)
	}
	return string(formatted), nil
}

func emitAccessMethod(source *strings.Builder, method analyzer.QueryMethod, query declaration.Query, sql string, packages map[string]string) {
	projections := queryProjections(query)
	returnsRows := len(projections) != 0
	rowType := packages[method.RowPackage] + "." + method.RowType
	returnType := "uint64"
	switch query.Cardinality {
	case declaration.CardinalityExactlyOne:
		returnType = rowType
	case declaration.CardinalityZeroOrOne:
		returnType = "*" + rowType
	case declaration.CardinalityMany:
		returnType = "[]" + rowType
	}
	fmt.Fprintf(source, "\nfunc (provider *Provider) %s(ctx context.Context, request %s.%s) (%s, error) {\nvar zero %s\nif provider == nil || provider.database == nil { return zero, db.ErrUnavailable }\nresult, err := provider.database.Exec(ctx, db.Plan{Statements: []db.Statement{{ID: %q, SQL: %s, ReturnsRows: %t, Cardinality: db.Cardinality(%q),\n", method.Name, packages[method.RequestPackage], method.RequestType, returnType, returnType, query.ID, strconv.Quote(sql), returnsRows, query.Cardinality)
	if !returnsRows {
		// Direct count methods impose no affected-row restriction, including zero.
		source.WriteString("Affected: db.AtLeastAffectedRows(0),\n")
	}
	source.WriteString("Args: []db.Argument{")
	// CompileQueries numbers parameters in the normalized ID order.
	for _, parameter := range query.Parameters {
		fmt.Fprintf(source, "db.%sArgument(request.%s),", accessValueField(parameter.Type), parameter.GoField)
	}
	source.WriteString("},")
	if !returnsRows {
		source.WriteString("}}})\nif err != nil { return zero, err }\nif len(result.Statements) != 1 || len(result.Statements[0].Rows) != 0 { return zero, db.ErrBackend }\nreturn result.Statements[0].AffectedRows, nil\n}\n")
		return
	}
	source.WriteString("ResultColumns: []db.ResultColumn{")
	results := make(map[string]declaration.ResultField, len(query.Results))
	for _, result := range query.Results {
		results[result.Source] = result
	}
	ordered := make([]declaration.ResultField, len(projections))
	for index, projection := range projections {
		name := projection.Column
		if projection.Alias != "" {
			name = projection.Alias
		}
		ordered[index] = results[name]
		kind, _ := databaseValueKind(ordered[index].Type)
		fmt.Fprintf(source, "{Kind: db.ValueKind(%q), Nullable: %t},", kind, ordered[index].Nullable)
	}
	source.WriteString("}}}})\nif err != nil { return zero, err }\nif len(result.Statements) != 1 || result.Statements[0].AffectedRows != 0 { return zero, db.ErrBackend }\nrows := result.Statements[0].Rows\n")
	switch query.Cardinality {
	case declaration.CardinalityExactlyOne:
		source.WriteString("if len(rows) == 0 { return zero, db.ErrNotFound }; if len(rows) != 1 { return zero, db.ErrBackend }\n")
	case declaration.CardinalityZeroOrOne:
		source.WriteString("if len(rows) == 0 { return nil, nil }; if len(rows) != 1 { return zero, db.ErrBackend }\n")
	}
	fmt.Fprintf(source, "decoded := make([]%s, len(rows))\nfor index, cells := range rows {\nif len(cells) != %d { return zero, db.ErrBackend }\nvar row %s\n", rowType, len(ordered), rowType)
	for index, result := range ordered {
		kind, _ := databaseValueKind(result.Type)
		cell := fmt.Sprintf("cells[%d]", index)
		if result.Nullable {
			fmt.Fprintf(source, "if %s.Kind != db.ValueNull {\n", cell)
		}
		fmt.Fprintf(source, "if %s.Kind != db.ValueKind(%q) { return zero, db.ErrBackend }\n", cell, kind)
		value := cell + "." + accessValueField(result.Type)
		if result.Type == declaration.TypeBytes {
			value = "append([]byte{}, " + value + "...)"
		}
		if result.Nullable {
			fmt.Fprintf(source, "value%d := %s\nrow.%s = &value%d\n}\n", index, value, result.GoField, index)
		} else {
			fmt.Fprintf(source, "row.%s = %s\n", result.GoField, value)
		}
	}
	source.WriteString("decoded[index] = row\n}\n")
	switch query.Cardinality {
	case declaration.CardinalityExactlyOne:
		source.WriteString("return decoded[0], nil\n}\n")
	case declaration.CardinalityZeroOrOne:
		source.WriteString("return &decoded[0], nil\n}\n")
	case declaration.CardinalityMany:
		source.WriteString("return decoded, nil\n}\n")
	}
}

func accessValueField(kind declaration.ScalarType) string {
	switch kind {
	case declaration.TypeBool:
		return "Bool"
	case declaration.TypeBytes:
		return "Bytes"
	case declaration.TypeFloat64:
		return "Float64"
	case declaration.TypeInt64:
		return "Int64"
	case declaration.TypeString:
		return "String"
	default:
		panic("validated access scalar kind")
	}
}
