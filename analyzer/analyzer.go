// Package analyzer discovers Data declaration roots from a bounded, read-only
// Go source snapshot. It never loads or executes package initialization.
package analyzer

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/plystra/data/declaration"
)

const (
	declarationImportPath = "github.com/plystra/data/declaration"
	defaultMaxRoots       = 1024
	defaultMaxNodes       = declaration.MaximumNodes
	defaultMaxImports     = declaration.MaximumImports
	defaultMaxNesting     = declaration.MaximumNesting
	defaultMaxSymbolBytes = declaration.MaximumSymbolBytes
	defaultMaxDiagnostics = 256
	defaultMaxDiagBytes   = 256 * 1024
	MaxStandardExports    = 1024
	MaxStandardBytes      = 128 * 1024 * 1024
	MaxStandardFileBytes  = 16 * 1024 * 1024
)

var (
	// ErrInvalidSnapshot identifies malformed analyzer input rather than an
	// authored source diagnostic.
	ErrInvalidSnapshot = errors.New("invalid Data analyzer snapshot")

	memberIDPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)+/v[1-9][0-9]*$`)
)

// SourceFile is one immutable source file in a module-relative snapshot.
// Path uses slash separators and is never resolved against the host filesystem.
type SourceFile struct {
	Path    string `json:"path"`
	Content []byte `json:"content"`
}

// RootEligibility declares whether a source package may contribute Data roots.
type RootEligibility string

const (
	RootEligible RootEligibility = "eligible"
	RootSupport  RootEligibility = "support"
)

// Package is one finite source package supplied by the Data command. Support
// packages provide types and imports but cannot contribute Data roots. The
// analyzer does not discover additional files or packages from disk.
type Package struct {
	ImportPath      string          `json:"import_path"`
	RootEligibility RootEligibility `json:"root_eligibility"`
	Files           []SourceFile    `json:"files"`
}

// StandardExport is selected-toolchain type data for one standard package.
type StandardExport struct {
	ImportPath string `json:"import_path"`
	Content    []byte `json:"content"`
}

// ResourceContract is a Resource contract already validated by the caller's
// discovery phase. The analyzer uses the exact package/type identity to check
// a Member access type without importing Core or CLI code.
type ResourceContract struct {
	ImportPath string `json:"import_path"`
	TypeName   string `json:"type_name"`
	ID         string `json:"id"`
}

// Snapshot is the complete read-only input to one analysis operation.
type Snapshot struct {
	Packages        []Package          `json:"packages"`
	StandardExports []StandardExport   `json:"standard_exports,omitempty"`
	Resources       []ResourceContract `json:"resources"`
	Limits          Limits             `json:"limits"`
	TargetGOARCH    string             `json:"-"`
}

// Limits bounds one analysis operation. Zero fields use the protocol defaults.
type Limits struct {
	MaxRoots           int `json:"max_roots"`
	MaxNodes           int `json:"max_nodes"`
	MaxImports         int `json:"max_imports"`
	MaxNesting         int `json:"max_nesting"`
	MaxSymbolBytes     int `json:"max_symbol_bytes"`
	MaxDiagnostics     int `json:"max_diagnostics"`
	MaxDiagnosticBytes int `json:"max_diagnostic_bytes"`
}

// Span is a deterministic module-relative source span.
type Span struct {
	Path        string `json:"path"`
	StartLine   int    `json:"start_line"`
	StartColumn int    `json:"start_column"`
	EndLine     int    `json:"end_line"`
	EndColumn   int    `json:"end_column"`
}

// Root is one statically recognized Data declaration root.
type Root struct {
	MemberID       string            `json:"member_id"`
	PackagePath    string            `json:"package_path"`
	FilePath       string            `json:"file_path"`
	Symbol         string            `json:"symbol"`
	Model          declaration.Model `json:"model"`
	AccessPackage  string            `json:"access_package,omitempty"`
	AccessType     string            `json:"access_type,omitempty"`
	AccessID       string            `json:"access_id,omitempty"`
	QueryMethods   []QueryMethod     `json:"query_methods,omitempty"`
	MigrationOnly  bool              `json:"migration_only"`
	SourceDigest   string            `json:"source_digest"`
	DeclarationPos Span              `json:"declaration_pos"`
}

// QueryMethod records the authored types needed to implement one validated
// directly callable query or returning mutation without reopening Project source.
type QueryMethod struct {
	Name           string                  `json:"name"`
	Cardinality    declaration.Cardinality `json:"cardinality"`
	RequestPackage string                  `json:"request_package"`
	RequestType    string                  `json:"request_type"`
	RowPackage     string                  `json:"row_package"`
	RowType        string                  `json:"row_type"`
}

// DiagnosticCode is a stable source-facing failure class.
type DiagnosticCode string

const (
	CodeInvalidRootShape       DiagnosticCode = "invalid-root-shape"
	CodeUnsupportedExpression  DiagnosticCode = "unsupported-expression"
	CodeUnsupportedCapability  DiagnosticCode = "unsupported-capability"
	CodeInvalidID              DiagnosticCode = "invalid-id"
	CodeDuplicateSymbol        DiagnosticCode = "duplicate-symbol"
	CodeUnresolvedReference    DiagnosticCode = "unresolved-reference"
	CodeReferenceKindMismatch  DiagnosticCode = "reference-kind-mismatch"
	CodeImportCycle            DiagnosticCode = "import-cycle"
	CodeAccessContractMismatch DiagnosticCode = "access-contract-mismatch"
	CodeLimitExceeded          DiagnosticCode = "limit-exceeded"
	CodeTypeCheck              DiagnosticCode = "type-check"
	CodeInvalidSnapshot        DiagnosticCode = "invalid-snapshot"
	CodeEmitInput              DiagnosticCode = "emit-input"
	CodeEmitPath               DiagnosticCode = "emit-path"
	CodeEmitBackend            DiagnosticCode = "emit-backend"
	CodeEmitArtifact           DiagnosticCode = "emit-artifact"
)

// Diagnostic is a bounded, deterministic explanation of an authored source
// rejection. Message text never contains source bytes or host paths.
type Diagnostic struct {
	Code        DiagnosticCode `json:"code"`
	Message     string         `json:"message"`
	Action      string         `json:"action"`
	PackagePath string         `json:"package_path,omitempty"`
	FilePath    string         `json:"file_path,omitempty"`
	MemberID    string         `json:"member_id,omitempty"`
	Symbol      string         `json:"symbol,omitempty"`
	Span        Span           `json:"span"`
}

// Result is the immutable output of Analyze.
type Result struct {
	Roots       []Root       `json:"roots"`
	Diagnostics []Diagnostic `json:"diagnostics"`
	Digest      string       `json:"digest"`
	Truncated   bool         `json:"truncated"`
	Valid       bool         `json:"valid"`
}

type parsedPackage struct {
	source   Package
	fset     *token.FileSet
	files    []*ast.File
	contents map[string][]byte
	info     *types.Info
	typed    *types.Package
	parseErr bool
}

type analysis struct {
	limits    Limits
	packages  map[string]*parsedPackage
	standard  map[string][]byte
	goarch    string
	resources map[string]ResourceContract
	seenRoots map[string]Root
	result    Result
}

type sourceImporter struct {
	owner        *analysis
	state        map[string]uint8
	standard     types.Importer
	active       string
	inputFailure map[string]bool
}

// Analyze parses and type-checks only the files supplied in snapshot, then
// discovers directive-bound declaration roots only in eligible packages.
func Analyze(snapshot Snapshot) (Result, error) {
	a, err := newAnalysis(snapshot)
	if err != nil {
		return Result{}, err
	}
	a.parsePackages()
	a.typeCheckPackages()
	a.discoverRoots()
	a.validateReferences()
	a.finish()
	return a.result, nil
}

// SnapshotDigest returns the identity of the validated snapshot inputs. It
// binds source content digests, package/file paths, root eligibility, Resource
// identities, and effective limits without embedding source bytes.
func SnapshotDigest(snapshot Snapshot) (string, error) {
	a, err := newAnalysis(snapshot)
	if err != nil {
		return "", err
	}
	type fileIdentity struct {
		Path   string `json:"path"`
		Digest string `json:"digest"`
		Bytes  int    `json:"bytes"`
	}
	type packageIdentity struct {
		ImportPath      string          `json:"import_path"`
		RootEligibility RootEligibility `json:"root_eligibility"`
		Files           []fileIdentity  `json:"files"`
	}
	packages := make([]packageIdentity, 0, len(snapshot.Packages))
	for _, pkg := range snapshot.Packages {
		identity := packageIdentity{ImportPath: pkg.ImportPath, RootEligibility: pkg.RootEligibility, Files: make([]fileIdentity, 0, len(pkg.Files))}
		for _, file := range pkg.Files {
			identity.Files = append(identity.Files, fileIdentity{Path: file.Path, Digest: digestBytes(file.Content), Bytes: len(file.Content)})
		}
		sort.Slice(identity.Files, func(i, j int) bool { return identity.Files[i].Path < identity.Files[j].Path })
		packages = append(packages, identity)
	}
	sort.Slice(packages, func(i, j int) bool { return packages[i].ImportPath < packages[j].ImportPath })
	type standardIdentity struct {
		ImportPath string `json:"import_path"`
		Digest     string `json:"digest"`
		Bytes      int    `json:"bytes"`
	}
	standard := make([]standardIdentity, 0, len(snapshot.StandardExports))
	for _, item := range snapshot.StandardExports {
		standard = append(standard, standardIdentity{ImportPath: item.ImportPath, Digest: digestBytes(item.Content), Bytes: len(item.Content)})
	}
	sort.Slice(standard, func(i, j int) bool { return standard[i].ImportPath < standard[j].ImportPath })
	resources := append([]ResourceContract(nil), snapshot.Resources...)
	sort.Slice(resources, func(i, j int) bool {
		if resources[i].ID != resources[j].ID {
			return resources[i].ID < resources[j].ID
		}
		if resources[i].ImportPath != resources[j].ImportPath {
			return resources[i].ImportPath < resources[j].ImportPath
		}
		return resources[i].TypeName < resources[j].TypeName
	})
	payload, err := json.Marshal(struct {
		Packages        []packageIdentity  `json:"packages"`
		StandardExports []standardIdentity `json:"standard_exports,omitempty"`
		Resources       []ResourceContract `json:"resources"`
		Limits          Limits             `json:"limits"`
	}{packages, standard, resources, a.limits})
	if err != nil {
		return "", fmt.Errorf("%w: snapshot digest: %v", ErrInvalidSnapshot, err)
	}
	return digestBytes(append([]byte("plystra.data.snapshot/v1\x00"), payload...)), nil
}

func newAnalysis(snapshot Snapshot) (*analysis, error) {
	limits := snapshot.Limits
	if limits.MaxRoots == 0 {
		limits.MaxRoots = defaultMaxRoots
	}
	if limits.MaxNodes == 0 {
		limits.MaxNodes = defaultMaxNodes
	}
	if limits.MaxImports == 0 {
		limits.MaxImports = defaultMaxImports
	}
	if limits.MaxNesting == 0 {
		limits.MaxNesting = defaultMaxNesting
	}
	if limits.MaxSymbolBytes == 0 {
		limits.MaxSymbolBytes = defaultMaxSymbolBytes
	}
	if limits.MaxDiagnostics == 0 {
		limits.MaxDiagnostics = defaultMaxDiagnostics
	}
	if limits.MaxDiagnosticBytes == 0 {
		limits.MaxDiagnosticBytes = defaultMaxDiagBytes
	}
	if limits.MaxRoots < 1 || limits.MaxNodes < 1 || limits.MaxImports < 1 || limits.MaxNesting < 1 || limits.MaxSymbolBytes < 1 || limits.MaxDiagnostics < 1 || limits.MaxDiagnosticBytes < 1 {
		return nil, fmt.Errorf("%w: limits must be positive", ErrInvalidSnapshot)
	}

	a := &analysis{
		limits:    limits,
		packages:  make(map[string]*parsedPackage, len(snapshot.Packages)),
		standard:  make(map[string][]byte, len(snapshot.StandardExports)),
		resources: make(map[string]ResourceContract, len(snapshot.Resources)),
		seenRoots: make(map[string]Root),
	}
	a.goarch = snapshot.TargetGOARCH
	if a.goarch == "" {
		a.goarch = runtime.GOARCH
	}
	if types.SizesFor("gc", a.goarch) == nil {
		return nil, fmt.Errorf("%w: unsupported target architecture", ErrInvalidSnapshot)
	}
	for _, pkg := range snapshot.Packages {
		if pkg.ImportPath == "" || a.packages[pkg.ImportPath] != nil {
			return nil, fmt.Errorf("%w: package %q", ErrInvalidSnapshot, pkg.ImportPath)
		}
		if pkg.RootEligibility != RootEligible && pkg.RootEligibility != RootSupport {
			return nil, fmt.Errorf("%w: package %q has invalid root eligibility", ErrInvalidSnapshot, pkg.ImportPath)
		}
		if len(pkg.Files) == 0 {
			return nil, fmt.Errorf("%w: package %q has no files", ErrInvalidSnapshot, pkg.ImportPath)
		}
		seenFiles := make(map[string]struct{}, len(pkg.Files))
		for _, file := range pkg.Files {
			if err := validatePath(file.Path); err != nil {
				return nil, fmt.Errorf("%w: %s", ErrInvalidSnapshot, err)
			}
			if _, exists := seenFiles[file.Path]; exists {
				return nil, fmt.Errorf("%w: duplicate file %q", ErrInvalidSnapshot, file.Path)
			}
			seenFiles[file.Path] = struct{}{}
			if !utf8.Valid(file.Content) {
				return nil, fmt.Errorf("%w: file %q is not valid UTF-8", ErrInvalidSnapshot, file.Path)
			}
		}
		a.packages[pkg.ImportPath] = &parsedPackage{source: pkg, contents: make(map[string][]byte, len(pkg.Files))}
	}
	if len(snapshot.StandardExports) > MaxStandardExports {
		return nil, fmt.Errorf("%w: too many standard exports", ErrInvalidSnapshot)
	}
	totalStandard := 0
	for _, item := range snapshot.StandardExports {
		firstSegment := strings.SplitN(item.ImportPath, "/", 2)[0]
		if item.ImportPath == "" || len(item.ImportPath) > 512 || item.ImportPath == "." || item.ImportPath == ".." ||
			strings.HasPrefix(item.ImportPath, "../") || strings.HasPrefix(item.ImportPath, "./") ||
			item.ImportPath == "unsafe" || path.IsAbs(item.ImportPath) ||
			path.Clean(item.ImportPath) != item.ImportPath || strings.ContainsAny(item.ImportPath, "\\:") || strings.Contains(firstSegment, ".") ||
			len(item.Content) == 0 || len(item.Content) > MaxStandardFileBytes ||
			a.packages[item.ImportPath] != nil || a.standard[item.ImportPath] != nil {
			return nil, fmt.Errorf("%w: invalid standard export", ErrInvalidSnapshot)
		}
		totalStandard += len(item.Content)
		if totalStandard > MaxStandardBytes {
			return nil, fmt.Errorf("%w: standard exports exceed byte limit", ErrInvalidSnapshot)
		}
		a.standard[item.ImportPath] = append([]byte(nil), item.Content...)
	}
	seenResourceIDs := make(map[string]struct{}, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		if resource.ImportPath == "" || !validTypeName(resource.TypeName) || !memberIDPattern.MatchString(resource.ID) {
			return nil, fmt.Errorf("%w: resource contract %q.%q", ErrInvalidSnapshot, resource.ImportPath, resource.TypeName)
		}
		key := resourceKey(resource.ImportPath, resource.TypeName)
		if _, exists := a.resources[key]; exists {
			return nil, fmt.Errorf("%w: duplicate resource contract %q", ErrInvalidSnapshot, key)
		}
		if _, exists := seenResourceIDs[resource.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate resource ID %q", ErrInvalidSnapshot, resource.ID)
		}
		seenResourceIDs[resource.ID] = struct{}{}
		a.resources[key] = resource
	}
	return a, nil
}

func validatePath(value string) error {
	if value == "" || strings.ContainsAny(value, "\\:") || path.IsAbs(value) || path.Clean(value) != value || value == "." || strings.HasPrefix(value, "../") {
		return fmt.Errorf("invalid module-relative path %q", value)
	}
	parts := strings.Split(value, "/")
	if len(parts) == 0 || parts[0] == "" {
		return fmt.Errorf("invalid module-relative path %q", value)
	}
	for _, part := range parts {
		name := strings.ToUpper(strings.TrimRight(strings.SplitN(part, ".", 2)[0], " "))
		if strings.TrimRight(part, ". ") != part || strings.ContainsAny(part, `<>"|?*`) ||
			name == "CON" || name == "PRN" || name == "AUX" || name == "NUL" ||
			name == "CONIN$" || name == "CONOUT$" ||
			(len(name) == 4 && (strings.HasPrefix(name, "COM") || strings.HasPrefix(name, "LPT")) && name[3] >= '1' && name[3] <= '9') {
			return fmt.Errorf("unsafe source path %q", value)
		}
		if part == ".git" || part == "vendor" || part == "testdata" || part == "dist" || part == "generated" {
			return fmt.Errorf("reserved source path %q", value)
		}
	}
	return nil
}

func (a *analysis) parsePackages() {
	for _, pkg := range a.packages {
		pkg.fset = token.NewFileSet()
		pkg.files = make([]*ast.File, 0, len(pkg.source.Files))
		for _, source := range pkg.source.Files {
			parsed, err := parser.ParseFile(pkg.fset, source.Path, source.Content, parser.ParseComments)
			pkg.contents[source.Path] = append([]byte(nil), source.Content...)
			if err != nil {
				pkg.parseErr = true
				a.addDiagnostic(pkg, source.Path, nil, CodeTypeCheck, "source parsing failed", "Fix the Go syntax in the reported source file.")
			}
			if parsed != nil {
				for _, imported := range parsed.Imports {
					value, err := strconv.Unquote(imported.Path.Value)
					if err == nil && value == "C" {
						pkg.parseErr = true
						a.addDiagnostic(pkg, source.Path, imported.Path, CodeUnsupportedCapability, "cgo package type input is unavailable", "Use a Go-only Data declaration or supply a supported cgo type-input route.")
					}
				}
				pkg.files = append(pkg.files, parsed)
			}
		}
		sort.Slice(pkg.files, func(i, j int) bool {
			return pkg.fset.Position(pkg.files[i].Pos()).Filename < pkg.fset.Position(pkg.files[j].Pos()).Filename
		})
	}
}

func (a *analysis) typeCheckPackages() {
	lookup := func(importPath string) (io.ReadCloser, error) {
		content, exists := a.standard[importPath]
		if !exists {
			return nil, fmt.Errorf("standard export is not present in the source snapshot")
		}
		return io.NopCloser(bytes.NewReader(content)), nil
	}
	imp := &sourceImporter{
		owner: a, state: make(map[string]uint8, len(a.packages)),
		standard:     importer.ForCompiler(token.NewFileSet(), "gc", lookup),
		inputFailure: make(map[string]bool),
	}
	for importPath := range a.packages {
		a.checkPackage(importPath, imp)
	}
}

func (a *analysis) checkPackage(importPath string, imp *sourceImporter) *types.Package {
	pkg := a.packages[importPath]
	if pkg == nil || pkg.parseErr || pkg.typed != nil {
		return pkg.typed
	}
	if len(pkg.files) == 0 {
		return nil
	}
	pkg.info = &types.Info{
		Types:      make(map[ast.Expr]types.TypeAndValue),
		Defs:       make(map[*ast.Ident]types.Object),
		Uses:       make(map[*ast.Ident]types.Object),
		Selections: make(map[*ast.SelectorExpr]*types.Selection),
		Scopes:     make(map[ast.Node]*types.Scope),
	}
	imp.state[importPath] = 1
	previous := imp.active
	imp.active = importPath
	checker := types.Config{Importer: imp, Sizes: types.SizesFor("gc", a.goarch)}
	typed, err := checker.Check(importPath, pkg.fset, pkg.files, pkg.info)
	imp.active = previous
	pkg.typed = typed
	if err != nil {
		if imp.inputFailure[importPath] {
			a.addPackageDiagnostic(pkg, CodeInvalidSnapshot, "package type input is unavailable", "Recreate the complete selected Go source and standard-library type snapshot before retrying.")
		} else {
			a.addPackageDiagnostic(pkg, CodeTypeCheck, "package type checking failed", "Fix the reported Go type error before analyzing Data declarations.")
		}
	}
	if typed != nil {
		imp.state[importPath] = 2
	} else {
		imp.state[importPath] = 0
	}
	return typed
}

func (i *sourceImporter) Import(importPath string) (*types.Package, error) {
	if pkg := i.owner.packages[importPath]; pkg != nil {
		if i.state[importPath] == 1 {
			return nil, fmt.Errorf("source package import cycle")
		}
		return i.owner.checkPackage(importPath, i), nil
	}
	if importPath == "unsafe" {
		return types.Unsafe, nil
	}
	if _, exists := i.owner.standard[importPath]; !exists {
		i.inputFailure[i.active] = true
		return nil, fmt.Errorf("import is not present in the source snapshot")
	}
	pkg, err := i.standard.Import(importPath)
	if err != nil {
		i.inputFailure[i.active] = true
	}
	return pkg, err
}

func (a *analysis) discoverRoots() {
	for _, pkg := range a.packages {
		if pkg.source.RootEligibility != RootEligible {
			continue
		}
		for _, file := range pkg.files {
			a.discoverFile(pkg, file)
		}
	}
	for _, pkg := range a.packages {
		if pkg.source.RootEligibility != RootEligible {
			continue
		}
		for _, file := range pkg.files {
			for _, group := range file.Comments {
				for _, comment := range group.List {
					if !isDataDirective(comment.Text) || a.directiveAttached(pkg, file, comment) {
						continue
					}
					a.addDiagnostic(pkg, fileName(pkg, file), comment, CodeInvalidRootShape, "Data directive is not attached to one package-level variable", "Place //plystra:data <member-id> immediately before one exported package-level variable.")
				}
			}
		}
	}
}

func (a *analysis) discoverFile(pkg *parsedPackage, file *ast.File) {
	for _, declarationNode := range file.Decls {
		gen, ok := declarationNode.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, specNode := range gen.Specs {
			spec, ok := specNode.(*ast.ValueSpec)
			if !ok {
				continue
			}
			comment := attachedDirective(pkg, file, spec)
			if comment == nil {
				continue
			}
			memberID, valid := parseDirective(comment.Text)
			if !valid {
				a.addDiagnostic(pkg, fileName(pkg, file), comment, CodeInvalidID, "Data member directive has an invalid identity", "Use a lowercase dotted semantic ID ending in /vN.")
				continue
			}
			a.inspectRoot(pkg, file, spec, memberID, comment)
		}
	}
}

func attachedDirective(pkg *parsedPackage, file *ast.File, spec *ast.ValueSpec) *ast.Comment {
	position := pkg.fset.Position(spec.Pos())
	var match *ast.Comment
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !isDataDirective(comment.Text) || pkg.fset.Position(comment.End()).Line+1 != position.Line {
				continue
			}
			if comment.End() <= spec.Pos() {
				match = comment
			}
		}
	}
	return match
}

func (a *analysis) directiveAttached(pkg *parsedPackage, file *ast.File, comment *ast.Comment) bool {
	for _, declarationNode := range file.Decls {
		gen, ok := declarationNode.(*ast.GenDecl)
		if !ok || gen.Tok != token.VAR {
			continue
		}
		for _, specNode := range gen.Specs {
			spec, ok := specNode.(*ast.ValueSpec)
			if ok && attachedDirective(pkg, file, spec) == comment {
				return true
			}
		}
	}
	return false
}

func isDataDirective(text string) bool {
	value := strings.TrimSpace(text)
	return value == "//plystra:data" || strings.HasPrefix(value, "//plystra:data ")
}

func parseDirective(text string) (string, bool) {
	value := strings.TrimSpace(text)
	if !strings.HasPrefix(value, "//plystra:data") {
		return "", false
	}
	fields := strings.Fields(strings.TrimPrefix(value, "//"))
	if len(fields) != 2 || fields[0] != "plystra:data" || !declaration.ValidMemberID(fields[1]) {
		return "", false
	}
	return fields[1], true
}

func (a *analysis) inspectRoot(pkg *parsedPackage, file *ast.File, spec *ast.ValueSpec, memberID string, comment *ast.Comment) {
	if len(spec.Names) != 1 || len(spec.Values) != 1 {
		a.addDiagnostic(pkg, fileName(pkg, file), spec, CodeInvalidRootShape, "Data root must declare one exported variable with one initializer", "Use one exported var with one direct Member[Access] composite literal.")
		return
	}
	name := spec.Names[0]
	if !ast.IsExported(name.Name) {
		a.addDiagnostic(pkg, fileName(pkg, file), name, CodeInvalidRootShape, "Data root variable must be exported", "Export the package-level variable named by the Data directive.")
		return
	}
	if pkg.info == nil {
		return
	}
	initializer, ok := spec.Values[0].(*ast.CompositeLit)
	if !ok || initializer.Type == nil {
		a.addDiagnostic(pkg, fileName(pkg, file), spec.Values[0], CodeInvalidRootShape, "Data root initializer must be a direct typed composite literal", "Use declaration.Member[Access]{...} directly as the initializer.")
		return
	}
	_, accessType, ok := directMemberType(pkg.info, initializer)
	if !ok || accessType == nil {
		a.addDiagnostic(pkg, fileName(pkg, file), initializer, CodeInvalidRootShape, "Data root type must be declaration.Member[Access]", "Use the exact exported declaration.Member[Access] type.")
		return
	}
	accessPackage, accessName, accessID, migrationOnly, ok := a.validateAccess(pkg, accessType)
	if !ok {
		a.addDiagnosticWithMember(pkg, fileName(pkg, file), initializer, CodeAccessContractMismatch, "Data Member access type is not a validated Resource contract or declaration.NoAccess", "Use a discovered canonical Resource contract or declaration.NoAccess.", memberID, name.Name)
		return
	}
	checker := literalChecker{analysis: a, packagePath: pkg.source.ImportPath, file: fileName(pkg, file), memberID: memberID, symbol: name.Name, nodes: 0}
	checker.check(initializer, 0)
	if checker.failed {
		return
	}
	model, err := decodeModel(pkg.info, initializer)
	if err != nil {
		code := CodeInvalidRootShape
		if errors.Is(err, declaration.ErrReference) {
			code = CodeUnresolvedReference
		}
		a.addDiagnosticWithMember(pkg, fileName(pkg, file), initializer, code, "Data declaration could not be normalized", "Correct the typed Data literal and its references before retrying.", memberID, name.Name)
		return
	}
	var queryMethods []QueryMethod
	if !migrationOnly {
		if err := validateAccessMethodNames(accessType, model); err != nil {
			a.addDiagnosticWithMember(pkg, fileName(pkg, file), initializer, CodeAccessContractMismatch, err.Error(), "Match each exported access Resource method to exactly one query or plan Method.", memberID, name.Name)
			return
		}
		queryMethods, err = validateQueryMethodShapes(accessType, model)
		if err != nil {
			a.addDiagnosticWithMember(pkg, fileName(pkg, file), initializer, CodeAccessContractMismatch, err.Error(), "Use a supported typed query method and map every request and row field explicitly.", memberID, name.Name)
			return
		}
	}
	if checker.imports > a.limits.MaxImports {
		a.addDiagnosticWithMember(pkg, fileName(pkg, file), initializer, CodeLimitExceeded, "Data member import limit exceeded", "Reduce the member Imports list to the advertised analysis limit.", memberID, name.Name)
		return
	}
	if previous, exists := a.seenRoots[memberID]; exists {
		a.addDiagnosticWithMember(pkg, fileName(pkg, file), name, CodeDuplicateSymbol, fmt.Sprintf("Data member identity duplicates %s", previous.FilePath), "Give each Data root a unique member identity.", memberID, name.Name)
		return
	}
	if len(a.seenRoots) >= a.limits.MaxRoots {
		a.addDiagnosticWithMember(pkg, fileName(pkg, file), name, CodeLimitExceeded, "Data root limit exceeded", "Reduce the effective Data declaration roots to the advertised limit.", memberID, name.Name)
		return
	}
	root := Root{
		MemberID:       memberID,
		PackagePath:    pkg.source.ImportPath,
		FilePath:       fileName(pkg, file),
		Symbol:         name.Name,
		Model:          model,
		AccessPackage:  accessPackage,
		AccessType:     accessName,
		AccessID:       accessID,
		QueryMethods:   queryMethods,
		MigrationOnly:  migrationOnly,
		SourceDigest:   digestBytes(pkg.contents[fileName(pkg, file)]),
		DeclarationPos: span(pkg.fset, initializer.Pos(), initializer.End()),
	}
	a.seenRoots[memberID] = root
	a.result.Roots = append(a.result.Roots, root)
}

func directMemberType(info *types.Info, literal *ast.CompositeLit) (types.Type, types.Type, bool) {
	if literal.Type == nil {
		return nil, nil, false
	}
	base, index := memberTypeSyntax(literal.Type)
	selector, ok := base.(*ast.SelectorExpr)
	if !ok || selector.Sel.Name != "Member" || len(index) != 1 {
		return nil, nil, false
	}
	identifier, ok := selector.X.(*ast.Ident)
	if !ok {
		return nil, nil, false
	}
	object, ok := info.Uses[identifier].(*types.PkgName)
	if !ok || object.Imported().Path() != declarationImportPath {
		return nil, nil, false
	}
	typed, ok := info.TypeOf(literal).(*types.Named)
	if !ok {
		return nil, nil, false
	}
	origin := typed.Origin()
	if origin.Obj().Pkg() == nil || origin.Obj().Pkg().Path() != declarationImportPath || origin.Obj().Name() != "Member" {
		return nil, nil, false
	}
	args := typed.TypeArgs()
	if args == nil || args.Len() != 1 {
		return nil, nil, false
	}
	return typed, args.At(0), true
}

func memberTypeSyntax(expr ast.Expr) (ast.Expr, []ast.Expr) {
	switch value := expr.(type) {
	case *ast.IndexExpr:
		return value.X, []ast.Expr{value.Index}
	case *ast.IndexListExpr:
		return value.X, value.Indices
	default:
		return expr, nil
	}
}

func (a *analysis) validateAccess(pkg *parsedPackage, access types.Type) (string, string, string, bool, bool) {
	named, ok := types.Unalias(access).(*types.Named)
	if !ok || named.Obj().Pkg() == nil {
		return "", "", "", false, false
	}
	object := named.Obj()
	if object.Pkg().Path() == declarationImportPath && object.Name() == "NoAccess" {
		return object.Pkg().Path(), object.Name(), "", true, true
	}
	key := resourceKey(object.Pkg().Path(), object.Name())
	resource, exists := a.resources[key]
	if !exists {
		return "", "", "", false, false
	}
	if _, ok := types.Unalias(named.Underlying()).(*types.Interface); !ok {
		return "", "", "", false, false
	}
	return resource.ImportPath, resource.TypeName, resource.ID, false, true
}

func validateAccessMethodNames(access types.Type, model declaration.Model) error {
	named := types.Unalias(access).(*types.Named)
	iface := types.Unalias(named.Underlying()).(*types.Interface).Complete()
	mapped := make(map[string]struct{}, len(model.Queries)+len(model.Plans))
	for _, query := range model.Queries {
		if query.Method != "" {
			mapped[query.Method] = struct{}{}
		}
	}
	for _, plan := range model.Plans {
		if plan.Method != "" {
			mapped[plan.Method] = struct{}{}
		}
	}
	for i := 0; i < iface.NumMethods(); i++ {
		method := iface.Method(i)
		if !method.Exported() || method.Name() == "Start" || method.Name() == "Stop" || method.Name() == "Shutdown" || method.Name() == "Close" {
			return fmt.Errorf("access Resource cannot expose method %q", method.Name())
		}
		if _, exists := mapped[method.Name()]; !exists {
			return fmt.Errorf("access Resource method %q has no query or plan mapping", method.Name())
		}
		delete(mapped, method.Name())
	}
	if len(mapped) != 0 {
		names := make([]string, 0, len(mapped))
		for name := range mapped {
			names = append(names, name)
		}
		sort.Strings(names)
		return fmt.Errorf("query or plan method %q is absent from the access Resource", names[0])
	}
	return nil
}

type literalChecker struct {
	analysis    *analysis
	packagePath string
	file        string
	memberID    string
	symbol      string
	nodes       int
	imports     int
	failed      bool
}

func (c *literalChecker) check(expr ast.Expr, depth int) {
	if c.failed {
		return
	}
	if depth > c.analysis.limits.MaxNesting {
		c.reject(expr, CodeLimitExceeded, "Data declaration nesting limit exceeded", "Reduce nested Data declaration literals to the advertised analysis limit.")
		return
	}
	c.nodes++
	if c.nodes > c.analysis.limits.MaxNodes {
		c.reject(expr, CodeLimitExceeded, "Data declaration node limit exceeded", "Reduce the Data declaration to the advertised analysis limit.")
		return
	}
	info := c.info()
	switch value := expr.(type) {
	case *ast.BasicLit:
		if value.Kind != token.INT && value.Kind != token.FLOAT && value.Kind != token.CHAR && value.Kind != token.STRING {
			c.reject(expr, CodeUnsupportedExpression, "Data declaration literal kind is unsupported", "Use a string, rune, Boolean, signed integer, or unsigned integer literal.")
		}
	case *ast.Ident:
		object := info.Uses[value]
		if object == types.Universe.Lookup(value.Name) && (value.Name == "true" || value.Name == "false") {
			return
		}
		constant, ok := object.(*types.Const)
		if !ok || !allowedConstantPackage(constant, c.packagePath) {
			c.reject(expr, CodeUnsupportedExpression, "Data declarations cannot read variables or initialization state", "Use a literal, an allowed constant, or an explicit declaration.Ref.")
		}
	case *ast.SelectorExpr:
		constant, ok := info.Uses[value.Sel].(*types.Const)
		if !ok || constant.Pkg() == nil || constant.Pkg().Path() != declarationImportPath || !isTypedConstant(constant) {
			c.reject(expr, CodeUnsupportedExpression, "Data declaration selector is not an exported Data constant", "Use an exact exported Data enum or scalar constant.")
		}
	case *ast.CompositeLit:
		c.checkComposite(value, depth)
	case *ast.BinaryExpr:
		if !allowedBinary(value.Op) || info.Types[value].Value == nil {
			c.reject(expr, CodeUnsupportedExpression, "Data declaration expression is not a permitted constant expression", "Use closed literals or constant arithmetic, comparison, or concatenation.")
			return
		}
		c.check(value.X, depth+1)
		c.check(value.Y, depth+1)
	case *ast.UnaryExpr:
		if value.Op != token.ADD && value.Op != token.SUB && value.Op != token.NOT {
			c.reject(expr, CodeUnsupportedExpression, "Data declaration unary expression is unsupported", "Use only signed numeric or Boolean constant expressions.")
			return
		}
		c.check(value.X, depth+1)
	case *ast.KeyValueExpr:
		c.check(value.Value, depth+1)
	default:
		c.reject(expr, CodeUnsupportedExpression, "Data declaration expression is unsupported", "Use the closed Data declaration literal grammar.")
	}
}

func (c *literalChecker) checkComposite(literal *ast.CompositeLit, depth int) {
	typeOf := c.info().TypeOf(literal)
	if !allowedCompositeType(typeOf) {
		c.reject(literal, CodeUnsupportedExpression, "Data declaration composite type is outside the closed grammar", "Use only exported declaration node types and their slices.")
		return
	}
	if isDeclarationStructType(typeOf) {
		for _, element := range literal.Elts {
			field, ok := element.(*ast.KeyValueExpr)
			if !ok {
				c.reject(element, CodeUnsupportedExpression, "Data struct literals must use keyed fields", "Use keyed fields on declaration.Member and nested Data nodes.")
				return
			}
			key, ok := field.Key.(*ast.Ident)
			if !ok {
				c.reject(field.Key, CodeUnsupportedExpression, "Data struct field keys must be identifiers", "Use exported declaration field names as keys.")
				return
			}
			if key.Name == "Imports" {
				if list, ok := field.Value.(*ast.CompositeLit); ok {
					c.imports += len(list.Elts)
				}
			}
			c.check(field.Value, depth+1)
		}
		return
	}
	for _, element := range literal.Elts {
		c.check(element, depth+1)
	}
}

func (c *literalChecker) info() *types.Info {
	return c.analysis.packages[c.packagePath].info
}

func (c *literalChecker) reject(expr ast.Expr, code DiagnosticCode, message, action string) {
	c.failed = true
	pkg := c.analysis.packages[c.packagePath]
	c.analysis.addDiagnosticWithMember(pkg, c.file, expr, code, message, action, c.memberID, c.symbol)
}

func allowedConstantPackage(constant *types.Const, packagePath string) bool {
	if constant == nil || constant.Pkg() == nil {
		return false
	}
	return isTypedConstant(constant) && (constant.Pkg().Path() == packagePath || constant.Pkg().Path() == declarationImportPath)
}

func isTypedConstant(constant *types.Const) bool {
	if constant == nil {
		return false
	}
	basic, ok := types.Unalias(constant.Type()).(*types.Basic)
	return !ok || basic.Info()&types.IsUntyped == 0
}

func allowedBinary(op token.Token) bool {
	switch op {
	case token.ADD, token.SUB, token.MUL, token.QUO, token.REM, token.SHL, token.SHR, token.AND, token.OR, token.XOR, token.AND_NOT, token.EQL, token.NEQ, token.LSS, token.LEQ, token.GTR, token.GEQ:
		return true
	default:
		return false
	}
}

func allowedCompositeType(value types.Type) bool {
	value = types.Unalias(value)
	if named, ok := value.(*types.Named); ok {
		return named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == declarationImportPath && allowedDeclarationName(named.Obj().Name())
	}
	if slice, ok := value.(*types.Slice); ok {
		return allowedElementType(slice.Elem())
	}
	if array, ok := value.(*types.Array); ok {
		return allowedElementType(array.Elem())
	}
	return false
}

func allowedElementType(value types.Type) bool {
	value = types.Unalias(value)
	if named, ok := value.(*types.Named); ok {
		if named.Obj().Pkg() == nil {
			return false
		}
		if named.Obj().Pkg().Path() != declarationImportPath {
			return named.Obj().Name() == "string"
		}
		return allowedDeclarationName(named.Obj().Name()) || named.Obj().Name() == "ScalarType"
	}
	basic, ok := value.(*types.Basic)
	return ok && basic.Kind() == types.String
}

func allowedDeclarationName(name string) bool {
	switch name {
	case "Member", "Table", "Column", "Constraint", "Index", "Query", "Conflict", "Join", "JoinCondition", "ColumnRef", "Parameter", "Assignment", "Projection", "ResultField", "Predicate", "Ordering", "Plan", "PlanStatement", "AffectedRows", "Migration", "MigrationOperation", "Ref", "Value", "AggregateKind", "ConflictAction":
		return true
	default:
		return false
	}
}

func isDeclarationStructType(value types.Type) bool {
	named, ok := types.Unalias(value).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == declarationImportPath && allowedDeclarationName(named.Obj().Name())
}

func (a *analysis) addPackageDiagnostic(pkg *parsedPackage, code DiagnosticCode, message, action string) {
	file := ""
	if len(pkg.source.Files) > 0 {
		file = pkg.source.Files[0].Path
	}
	var node ast.Node
	if len(pkg.files) > 0 {
		node = pkg.files[0]
	}
	a.addDiagnostic(pkg, file, node, code, message, action)
}

func (a *analysis) addDiagnostic(pkg *parsedPackage, file string, node ast.Node, code DiagnosticCode, message, action string) {
	a.addDiagnosticWithMember(pkg, file, node, code, message, action, "", "")
}

func (a *analysis) addDiagnosticWithMember(pkg *parsedPackage, file string, node ast.Node, code DiagnosticCode, message, action, memberID, symbol string) {
	diagnostic := Diagnostic{
		Code:        code,
		Message:     message,
		Action:      action,
		PackagePath: pkg.source.ImportPath,
		FilePath:    file,
		MemberID:    memberID,
		Symbol:      symbol,
	}
	if node != nil && pkg.fset != nil {
		diagnostic.Span = span(pkg.fset, node.Pos(), node.End())
	} else {
		diagnostic.Span.Path = file
	}
	a.result.Diagnostics = append(a.result.Diagnostics, diagnostic)
}

func span(fset *token.FileSet, start, end token.Pos) Span {
	startPosition := fset.Position(start)
	endPosition := fset.Position(end)
	return Span{Path: startPosition.Filename, StartLine: startPosition.Line, StartColumn: startPosition.Column, EndLine: endPosition.Line, EndColumn: endPosition.Column}
}

func fileName(pkg *parsedPackage, file *ast.File) string {
	return pkg.fset.Position(file.Pos()).Filename
}

func resourceKey(importPath, typeName string) string {
	return importPath + "\x00" + typeName
}

func validTypeName(value string) bool {
	if value == "" || !ast.IsExported(value) {
		return false
	}
	for index, r := range value {
		if index == 0 {
			if r != '_' && !unicode.IsLetter(r) {
				return false
			}
			continue
		}
		if r != '_' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (a *analysis) trimDiagnostics() {
	bytesUsed := 0
	withinLimit := true
	for _, diagnostic := range a.result.Diagnostics {
		encoded, err := json.Marshal(diagnostic)
		if err != nil || bytesUsed+len(encoded) > a.limits.MaxDiagnosticBytes {
			withinLimit = false
			break
		}
		bytesUsed += len(encoded)
	}
	if len(a.result.Diagnostics) <= a.limits.MaxDiagnostics && withinLimit {
		return
	}
	truncated := make([]Diagnostic, 0, a.limits.MaxDiagnostics)
	bytesUsed = 0
	for _, diagnostic := range a.result.Diagnostics {
		encoded, err := json.Marshal(diagnostic)
		if err != nil || len(truncated) == a.limits.MaxDiagnostics || bytesUsed+len(encoded) > a.limits.MaxDiagnosticBytes {
			break
		}
		truncated = append(truncated, diagnostic)
		bytesUsed += len(encoded)
	}
	a.result.Diagnostics = truncated
	a.result.Truncated = true
}

func (a *analysis) finish() {
	sort.Slice(a.result.Roots, func(i, j int) bool {
		if a.result.Roots[i].MemberID != a.result.Roots[j].MemberID {
			return a.result.Roots[i].MemberID < a.result.Roots[j].MemberID
		}
		if a.result.Roots[i].PackagePath != a.result.Roots[j].PackagePath {
			return a.result.Roots[i].PackagePath < a.result.Roots[j].PackagePath
		}
		if a.result.Roots[i].FilePath != a.result.Roots[j].FilePath {
			return a.result.Roots[i].FilePath < a.result.Roots[j].FilePath
		}
		if a.result.Roots[i].DeclarationPos.StartLine != a.result.Roots[j].DeclarationPos.StartLine {
			return a.result.Roots[i].DeclarationPos.StartLine < a.result.Roots[j].DeclarationPos.StartLine
		}
		if a.result.Roots[i].DeclarationPos.StartColumn != a.result.Roots[j].DeclarationPos.StartColumn {
			return a.result.Roots[i].DeclarationPos.StartColumn < a.result.Roots[j].DeclarationPos.StartColumn
		}
		return a.result.Roots[i].Symbol < a.result.Roots[j].Symbol
	})
	sort.SliceStable(a.result.Diagnostics, func(i, j int) bool {
		left, right := a.result.Diagnostics[i], a.result.Diagnostics[j]
		if left.PackagePath != right.PackagePath {
			return left.PackagePath < right.PackagePath
		}
		if left.FilePath != right.FilePath {
			return left.FilePath < right.FilePath
		}
		if left.Span.StartLine != right.Span.StartLine {
			return left.Span.StartLine < right.Span.StartLine
		}
		if left.Span.StartColumn != right.Span.StartColumn {
			return left.Span.StartColumn < right.Span.StartColumn
		}
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.MemberID != right.MemberID {
			return left.MemberID < right.MemberID
		}
		return left.Symbol < right.Symbol
	})
	a.trimDiagnostics()
	a.result.Digest, _ = ResultDigest(a.result.Roots, a.result.Diagnostics, a.result.Truncated)
	// A truncated diagnostic set still represents a rejected declaration. The
	// caller must not treat an omitted diagnostic as proof that analysis passed.
	a.result.Valid = len(a.result.Diagnostics) == 0 && !a.result.Truncated
}

// ResultDigest returns the stable identity of one complete analysis result.
// Emit requests use it to prove that the accepted pre-Freeze result was not
// replaced while the frozen application inputs were being prepared.
func ResultDigest(roots []Root, diagnostics []Diagnostic, truncated bool) (string, error) {
	payload, err := json.Marshal(struct {
		Roots       []Root       `json:"roots"`
		Diagnostics []Diagnostic `json:"diagnostics"`
		Truncated   bool         `json:"truncated"`
	}{roots, diagnostics, truncated})
	if err != nil {
		return "", err
	}
	return digestBytes(append([]byte("plystra.data.analyze/v1\x00"), payload...)), nil
}
