// Package protocol defines the bounded Data compiler analyze protocol.
package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
)

const (
	DistributionSchema   = "plystra.data-compiler-distribution/v1"
	AnalyzeSchema        = "plystra.data-analyze/v1"
	EmitSchema           = "plystra.data-emit/v1"
	DeclarationLanguage  = "plystra.data-declaration/v1"
	AnalyzePhase         = "analyze"
	EmitPhase            = "emit"
	ModulePath           = "github.com/plystra/data"
	CommandImportPath    = "github.com/plystra/data/cmd/plystra-data-compiler"
	MaxFrameBytes        = 16 * 1024 * 1024
	MaxSnapshotFiles     = 4096
	MaxSnapshotBytes     = 64 * 1024 * 1024
	MaxSourceFileBytes   = 4 * 1024 * 1024
	MaxSourcePathBytes   = 512
	MaxStandardExports   = analyzer.MaxStandardExports
	MaxStandardBytes     = analyzer.MaxStandardBytes
	MaxStandardFileBytes = analyzer.MaxStandardFileBytes
	MaxArtifacts         = 4096
	MaxArtifactBytes     = 4 * 1024 * 1024
	MaxArtifactPathBytes = 512
)

var (
	ErrFrameTooLarge = errors.New("Data compiler protocol frame exceeds the limit")
	ErrInvalidFrame  = errors.New("invalid Data compiler protocol frame")
)

// Status is the terminal state of a compiler request.
type Status string

const (
	StatusSucceeded Status = "succeeded"
	StatusRejected  Status = "rejected"
	StatusFailed    Status = "failed"
)

// CompilerIdentity is the exact distribution identity supplied by the Data command.
type CompilerIdentity struct {
	ModulePath          string `json:"module_path"`
	ModuleVersion       string `json:"module_version"`
	ModuleChecksum      string `json:"module_checksum"`
	ManifestDigest      string `json:"manifest_digest"`
	CommandImportPath   string `json:"command_import_path"`
	AnalyzeProtocol     string `json:"analyze_protocol"`
	EmitProtocol        string `json:"emit_protocol"`
	DeclarationLanguage string `json:"declaration_language"`
	GoToolchain         string `json:"go_toolchain"`
}

// BuildContext identifies the selected Go type-checking context.
type BuildContext struct {
	GOOS      string   `json:"goos"`
	GOARCH    string   `json:"goarch"`
	BuildTags []string `json:"build_tags,omitempty"`
}

// AnalyzeRequest is the complete pre-Freeze analyze input. The compiler reads
// only the verified private source snapshot named by the manifest.
type AnalyzeRequest struct {
	Schema      string           `json:"schema"`
	Phase       string           `json:"phase"`
	RequestID   string           `json:"request_id"`
	Compiler    CompilerIdentity `json:"compiler"`
	Build       BuildContext     `json:"build"`
	Limits      analyzer.Limits  `json:"limits"`
	InputDigest string           `json:"input_digest"`
	Snapshot    SnapshotManifest `json:"snapshot"`
}

// AnalyzeOutput contains no generated file or application-graph mutation.
type AnalyzeOutput struct {
	Roots     []analyzer.Root `json:"roots"`
	Digest    string          `json:"digest"`
	Truncated bool            `json:"truncated"`
	Valid     bool            `json:"valid"`
}

// AnalyzeResponse is the terminal response for one AnalyzeRequest.
type AnalyzeResponse struct {
	Schema       string                `json:"schema"`
	Phase        string                `json:"phase"`
	RequestID    string                `json:"request_id"`
	InputDigest  string                `json:"input_digest"`
	Status       Status                `json:"status"`
	OutputDigest string                `json:"output_digest,omitempty"`
	Output       AnalyzeOutput         `json:"output"`
	Diagnostics  []analyzer.Diagnostic `json:"diagnostics"`
	Truncated    bool                  `json:"truncated"`
}

// WriteFrame writes one canonical JSON payload prefixed by a big-endian
// uint32 payload length. It never writes protocol text or logging output.
func WriteFrame(w io.Writer, value any) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("%w: marshal: %v", ErrInvalidFrame, err)
	}
	if len(payload) == 0 || len(payload) > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(payload)))
	if err := writeAll(w, header[:]); err != nil {
		return fmt.Errorf("write frame header: %w", err)
	}
	if err := writeAll(w, payload); err != nil {
		return fmt.Errorf("write frame payload: %w", err)
	}
	return nil
}

func writeAll(w io.Writer, payload []byte) error {
	for len(payload) > 0 {
		written, err := w.Write(payload)
		if err != nil {
			return err
		}
		if written <= 0 || written > len(payload) {
			return io.ErrShortWrite
		}
		payload = payload[written:]
	}
	return nil
}

// ReadFrame reads one strict JSON frame. Unknown fields and trailing JSON are
// rejected so the protocol cannot silently accept an altered contract.
func ReadFrame(r io.Reader, value any) error {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	size := binary.BigEndian.Uint32(header[:])
	if size == 0 {
		return fmt.Errorf("%w: empty payload", ErrInvalidFrame)
	}
	if size > MaxFrameBytes {
		return ErrFrameTooLarge
	}
	payload := make([]byte, size)
	if _, err := io.ReadFull(r, payload); err != nil {
		return fmt.Errorf("%w: truncated payload", ErrInvalidFrame)
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: decode: %v", ErrInvalidFrame, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: trailing JSON", ErrInvalidFrame)
	}
	return nil
}

// AnalyzeInputDigest binds the immutable source snapshot to the exact selected
// Go build context. Build tags are treated as a set for deterministic identity.
func AnalyzeInputDigest(snapshot SnapshotManifest, build BuildContext) (string, error) {
	snapshotDigest, err := SnapshotManifestDigest(snapshot)
	if err != nil {
		return "", err
	}
	tags := append([]string(nil), build.BuildTags...)
	sort.Strings(tags)
	payload, err := json.Marshal(struct {
		SnapshotDigest string       `json:"snapshot_digest"`
		Build          BuildContext `json:"build"`
	}{
		SnapshotDigest: snapshotDigest,
		Build: BuildContext{
			GOOS:      build.GOOS,
			GOARCH:    build.GOARCH,
			BuildTags: tags,
		},
	})
	if err != nil {
		return "", fmt.Errorf("analyze input digest: %w", err)
	}
	sum := sha256.Sum256(append([]byte("plystra.data.analyze-input/v1\x00"), payload...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// ValidateAnalyzeRequest checks protocol identity and binds the request to its
// exact source snapshot and Go build context before analysis starts.
func ValidateAnalyzeRequest(request AnalyzeRequest) error {
	if request.Schema != AnalyzeSchema || request.Phase != AnalyzePhase {
		return fmt.Errorf("unsupported analyze schema or phase")
	}
	if strings.TrimSpace(request.RequestID) == "" {
		return fmt.Errorf("request ID is required")
	}
	if err := validateCompilerAndBuild(request.Compiler, request.Build); err != nil {
		return err
	}
	expected, err := AnalyzeInputDigest(request.Snapshot, request.Build)
	if err != nil {
		return err
	}
	if request.InputDigest != expected {
		return fmt.Errorf("input digest does not match the supplied snapshot")
	}
	if request.Limits != request.Snapshot.Limits {
		return fmt.Errorf("request limits do not match snapshot limits")
	}
	return nil
}

// HandleAnalyze validates and executes exactly one analyze request.
func HandleAnalyze(request AnalyzeRequest) AnalyzeResponse {
	response := AnalyzeResponse{Schema: AnalyzeSchema, Phase: AnalyzePhase, RequestID: request.RequestID, InputDigest: request.InputDigest, Output: AnalyzeOutput{}}
	if err := ValidateAnalyzeRequest(request); err != nil {
		response.Status = StatusRejected
		response.Diagnostics = []analyzer.Diagnostic{{Code: analyzer.CodeInvalidSnapshot, Message: "analyze request rejected", Action: "Correct the compiler identity, build context, limits, and source snapshot before retrying."}}
		return response
	}
	snapshot, err := LoadSnapshot(request.Snapshot)
	if err != nil {
		response.Status = StatusRejected
		response.Diagnostics = []analyzer.Diagnostic{{Code: analyzer.CodeInvalidSnapshot, Message: "analyze snapshot rejected", Action: "Recreate the private source snapshot before retrying."}}
		return response
	}
	snapshot.TargetGOARCH = request.Build.GOARCH
	result, err := analyzer.Analyze(snapshot)
	if err != nil {
		response.Status = StatusRejected
		response.Diagnostics = []analyzer.Diagnostic{{Code: analyzer.CodeInvalidSnapshot, Message: "analyze snapshot rejected", Action: "Correct the bounded source snapshot before retrying."}}
		return response
	}
	response.Output = AnalyzeOutput{Roots: result.Roots, Digest: result.Digest, Truncated: result.Truncated, Valid: result.Valid}
	response.OutputDigest = result.Digest
	response.Diagnostics = result.Diagnostics
	response.Truncated = result.Truncated
	if result.Valid {
		response.Status = StatusSucceeded
	} else {
		response.Status = StatusRejected
	}
	return response
}

// ValidateAnalyzeResponse checks a compiler result before the Data command
// accepts any discovered member or diagnostic as Project analysis output.
func ValidateAnalyzeResponse(request AnalyzeRequest, response AnalyzeResponse) error {
	if err := ValidateAnalyzeRequest(request); err != nil {
		return fmt.Errorf("invalid analyze request: %w", err)
	}
	if response.Schema != AnalyzeSchema || response.Phase != AnalyzePhase || response.RequestID != request.RequestID || response.InputDigest != request.InputDigest || response.Truncated != response.Output.Truncated {
		return fmt.Errorf("analyze response identity or truncation mismatch")
	}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > MaxFrameBytes {
		return fmt.Errorf("analyze response frame exceeds the limit")
	}
	if err := validateDiagnostics(response.Diagnostics, request.Limits); err != nil {
		return err
	}
	if err := validateAnalyzeDiagnostics(request.Snapshot, response.Diagnostics); err != nil {
		return err
	}
	if response.Status != StatusSucceeded && response.Status != StatusRejected && response.Status != StatusFailed {
		return fmt.Errorf("analyze response status is invalid")
	}
	if response.OutputDigest == "" {
		if response.Status == StatusSucceeded || response.Output.Digest != "" || len(response.Output.Roots) != 0 || response.Output.Valid || response.Truncated || len(response.Diagnostics) == 0 {
			return fmt.Errorf("analyze response is missing its output digest")
		}
		return nil
	}
	if response.Status == StatusFailed || !validDigest(response.OutputDigest) || response.Output.Digest != response.OutputDigest {
		return fmt.Errorf("analyze response digest or status is invalid")
	}
	maximumRoots := request.Limits.MaxRoots
	if maximumRoots == 0 {
		maximumRoots = MaxRoots
	}
	if len(response.Output.Roots) > maximumRoots {
		return fmt.Errorf("analyze root count exceeds the limit")
	}
	if err := validateAnalyzeRoots(request.Snapshot, response.Output.Roots); err != nil {
		return err
	}
	digest, err := analyzer.ResultDigest(response.Output.Roots, response.Diagnostics, response.Truncated)
	if err != nil || digest != response.OutputDigest {
		return fmt.Errorf("analyze output digest mismatch")
	}
	valid := len(response.Diagnostics) == 0 && !response.Truncated
	if response.Output.Valid != valid || (response.Status == StatusSucceeded) != valid {
		return fmt.Errorf("analyze response validity or status mismatch")
	}
	return nil
}

func validateAnalyzeRoots(snapshot SnapshotManifest, roots []analyzer.Root) error {
	packages := make(map[string]SnapshotPackage, len(snapshot.Packages))
	for _, pkg := range snapshot.Packages {
		packages[pkg.ImportPath] = pkg
	}
	resources := make(map[string]string, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		resources[resource.ImportPath+"\x00"+resource.TypeName] = resource.ID
	}
	previous := ""
	for _, root := range roots {
		if !declaration.ValidMemberID(root.MemberID) || root.MemberID <= previous || root.Symbol == "" || root.DeclarationPos.Path != root.FilePath || root.DeclarationPos.StartLine < 1 || root.DeclarationPos.StartColumn < 1 {
			return fmt.Errorf("analyze root identity or order is invalid")
		}
		previous = root.MemberID
		pkg, exists := packages[root.PackagePath]
		if !exists || pkg.RootEligibility != analyzer.RootEligible {
			return fmt.Errorf("analyze root package is not eligible")
		}
		matched := false
		for _, file := range pkg.Files {
			if file.Path == root.FilePath && file.Digest == root.SourceDigest {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("analyze root source is not in the snapshot")
		}
		if root.MigrationOnly {
			if root.AccessPackage != "github.com/plystra/data/declaration" || root.AccessType != "NoAccess" || root.AccessID != "" || len(root.QueryMethods) != 0 {
				return fmt.Errorf("analyze migration-only access identity is invalid")
			}
		} else if root.AccessPackage == "" || root.AccessType == "" || root.AccessID == "" || resources[root.AccessPackage+"\x00"+root.AccessType] != root.AccessID {
			return fmt.Errorf("analyze root access identity is not in the snapshot")
		} else if err := validateQueryMethods(packages, root); err != nil {
			return err
		}
		model, err := (declaration.Member[struct{}]{
			Namespace: root.Model.Namespace, Imports: root.Model.Imports,
			Tables: root.Model.Tables, Queries: root.Model.Queries,
			Plans: root.Model.Plans, Migrations: root.Model.Migrations,
		}).Normalize()
		if err != nil || !jsonEqual(model, root.Model) {
			return fmt.Errorf("analyze root model is not normalized")
		}
	}
	return nil
}

func validateQueryMethods(packages map[string]SnapshotPackage, root analyzer.Root) error {
	queries := make(map[string]declaration.Query)
	for _, query := range root.Model.Queries {
		if query.Method != "" {
			queries[query.Method] = query
		}
	}
	for _, plan := range root.Model.Plans {
		if plan.Method != "" {
			return fmt.Errorf("analyze root has an unsupported access plan method")
		}
	}
	if len(queries) == 0 || len(root.QueryMethods) != len(queries) {
		return fmt.Errorf("analyze root query method inventory is incomplete")
	}
	previous := ""
	for _, method := range root.QueryMethods {
		query, exists := queries[method.Name]
		if !exists || method.Name <= previous || method.Cardinality != query.Cardinality ||
			!validAccessTypeName(method.RequestType) || packages[method.RequestPackage].ImportPath == "" {
			return fmt.Errorf("analyze root query method shape is invalid")
		}
		if query.Cardinality == declaration.CardinalityNone {
			if query.Operation == declaration.OperationSelect || len(query.Returning) != 0 || method.RowPackage != "" || method.RowType != "" {
				return fmt.Errorf("analyze root affected-row method shape is invalid")
			}
		} else if !validAccessTypeName(method.RowType) || packages[method.RowPackage].ImportPath == "" {
			return fmt.Errorf("analyze root query row type is invalid")
		}
		previous = method.Name
	}
	return nil
}

func validAccessTypeName(value string) bool {
	first, _ := utf8.DecodeRuneInString(value)
	return len(value) <= declaration.MaximumSymbolBytes && token.IsIdentifier(value) && unicode.IsUpper(first)
}

func validateAnalyzeDiagnostics(snapshot SnapshotManifest, diagnostics []analyzer.Diagnostic) error {
	packages := make(map[string]SnapshotPackage, len(snapshot.Packages))
	for _, pkg := range snapshot.Packages {
		packages[pkg.ImportPath] = pkg
	}
	for _, diagnostic := range diagnostics {
		if diagnostic.Message == "" || diagnostic.Action == "" || !validAnalyzeDiagnosticCode(diagnostic.Code) {
			return fmt.Errorf("analyze diagnostic identity is invalid")
		}
		if diagnostic.PackagePath == "" {
			if diagnostic.FilePath != "" || diagnostic.Span.Path != "" || diagnostic.MemberID != "" || diagnostic.Symbol != "" {
				return fmt.Errorf("analyze diagnostic has unowned provenance")
			}
			continue
		}
		pkg, exists := packages[diagnostic.PackagePath]
		if !exists || diagnostic.FilePath == "" || diagnostic.Span.Path != diagnostic.FilePath {
			return fmt.Errorf("analyze diagnostic package or path is not in the snapshot")
		}
		matched := false
		for _, file := range pkg.Files {
			if file.Path == diagnostic.FilePath {
				matched = true
				break
			}
		}
		if !matched || (diagnostic.MemberID != "" && !declaration.ValidMemberID(diagnostic.MemberID)) {
			return fmt.Errorf("analyze diagnostic source is not in the snapshot")
		}
	}
	return nil
}

func validAnalyzeDiagnosticCode(code analyzer.DiagnosticCode) bool {
	switch code {
	case analyzer.CodeInvalidRootShape, analyzer.CodeUnsupportedExpression, analyzer.CodeUnsupportedCapability,
		analyzer.CodeInvalidID, analyzer.CodeDuplicateSymbol, analyzer.CodeUnresolvedReference,
		analyzer.CodeReferenceKindMismatch, analyzer.CodeImportCycle, analyzer.CodeAccessContractMismatch,
		analyzer.CodeLimitExceeded, analyzer.CodeTypeCheck, analyzer.CodeInvalidSnapshot:
		return true
	default:
		return false
	}
}

// Run serves length-delimited requests until stdin reaches EOF.
func Run(in io.Reader, out io.Writer) error {
	for {
		var raw json.RawMessage
		err := ReadFrame(in, &raw)
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		var envelope struct {
			Phase string `json:"phase"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return fmt.Errorf("%w: phase: %v", ErrInvalidFrame, err)
		}
		var response any
		switch envelope.Phase {
		case AnalyzePhase:
			var request AnalyzeRequest
			if err := decodeRequest(raw, &request); err != nil {
				return err
			}
			response = HandleAnalyze(request)
		case EmitPhase:
			var request EmitRequest
			if err := decodeRequest(raw, &request); err != nil {
				return err
			}
			response = HandleEmit(request)
		default:
			return fmt.Errorf("%w: unsupported phase", ErrInvalidFrame)
		}
		if err := WriteFrame(out, response); err != nil {
			return err
		}
	}
}

func decodeRequest(payload []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return fmt.Errorf("%w: decode request: %v", ErrInvalidFrame, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("%w: trailing request JSON", ErrInvalidFrame)
	}
	return nil
}

func validateCompilerAndBuild(identity CompilerIdentity, build BuildContext) error {
	if identity.ModulePath != ModulePath || strings.TrimSpace(identity.ModuleVersion) == "" || identity.CommandImportPath != CommandImportPath || identity.AnalyzeProtocol != AnalyzeSchema || identity.EmitProtocol != EmitSchema || identity.DeclarationLanguage != DeclarationLanguage || strings.TrimSpace(identity.GoToolchain) == "" {
		return fmt.Errorf("compiler identity is incomplete or incompatible")
	}
	if !validModuleChecksum(identity.ModuleChecksum) || !validDigest(identity.ManifestDigest) {
		return fmt.Errorf("compiler identity digest is invalid")
	}
	if strings.TrimSpace(build.GOOS) == "" || strings.TrimSpace(build.GOARCH) == "" {
		return fmt.Errorf("Go build context is incomplete")
	}
	return nil
}

func validModuleChecksum(value string) bool {
	if !strings.HasPrefix(value, "h1:") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "h1:"))
	return err == nil && len(decoded) == sha256.Size
}

func validDigest(value string) bool {
	if len(value) != len("sha256:")+64 || !strings.HasPrefix(value, "sha256:") {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}
