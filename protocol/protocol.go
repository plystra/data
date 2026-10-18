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
	"io"
	"sort"
	"strings"

	"github.com/plystra/data/analyzer"
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

// AnalyzeRequest is the complete pre-Freeze analyze input. The source
// snapshot is self-contained so the compiler never reads a host checkout.
type AnalyzeRequest struct {
	Schema      string            `json:"schema"`
	Phase       string            `json:"phase"`
	RequestID   string            `json:"request_id"`
	Compiler    CompilerIdentity  `json:"compiler"`
	Build       BuildContext      `json:"build"`
	Limits      analyzer.Limits   `json:"limits"`
	InputDigest string            `json:"input_digest"`
	Snapshot    analyzer.Snapshot `json:"snapshot"`
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
func AnalyzeInputDigest(snapshot analyzer.Snapshot, build BuildContext) (string, error) {
	snapshotDigest, err := analyzer.SnapshotDigest(snapshot)
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
	result, err := analyzer.Analyze(request.Snapshot)
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
