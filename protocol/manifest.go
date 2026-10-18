package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/plystra/data/declaration"
)

const (
	MaxRoots           = 1024
	MaxNodes           = declaration.MaximumNodes
	MaxImports         = declaration.MaximumImports
	MaxNesting         = declaration.MaximumNesting
	MaxSymbolBytes     = declaration.MaximumSymbolBytes
	MaxDiagnostics     = 256
	MaxDiagnosticBytes = 256 * 1024
)

var ErrInvalidDistributionManifest = errors.New("invalid Data compiler distribution manifest")

// DistributionLimits declares every bounded input, diagnostic, and artifact
// value supported by the Data compiler distribution.
type DistributionLimits struct {
	MaxRoots             int `json:"max_roots"`
	MaxNodes             int `json:"max_nodes"`
	MaxImports           int `json:"max_imports"`
	MaxNesting           int `json:"max_nesting"`
	MaxSymbolBytes       int `json:"max_symbol_bytes"`
	MaxDiagnostics       int `json:"max_diagnostics"`
	MaxDiagnosticBytes   int `json:"max_diagnostic_bytes"`
	MaxFrameBytes        int `json:"max_frame_bytes"`
	MaxSnapshotFiles     int `json:"max_snapshot_files"`
	MaxSnapshotBytes     int `json:"max_snapshot_bytes"`
	MaxSourceFileBytes   int `json:"max_source_file_bytes"`
	MaxSourcePathBytes   int `json:"max_source_path_bytes"`
	MaxArtifacts         int `json:"max_artifacts"`
	MaxArtifactBytes     int `json:"max_artifact_bytes"`
	MaxArtifactPathBytes int `json:"max_artifact_path_bytes"`
}

// DistributionManifest is the closed identity and bounds contract published
// with one Data compiler module version.
type DistributionManifest struct {
	Schema              string             `json:"schema"`
	ModulePath          string             `json:"module_path"`
	CommandImportPath   string             `json:"command_import_path"`
	AnalyzeProtocol     string             `json:"analyze_protocol"`
	EmitProtocol        string             `json:"emit_protocol"`
	DeclarationLanguage string             `json:"declaration_language"`
	Limits              DistributionLimits `json:"limits"`
}

// ParseDistributionManifest parses one strict distribution manifest and
// returns the digest of the exact published bytes. Unknown fields, duplicate
// fields, and trailing JSON are rejected before validation.
func ParseDistributionManifest(data []byte) (DistributionManifest, string, error) {
	if len(data) == 0 || len(data) > MaxFrameBytes {
		return DistributionManifest{}, "", fmt.Errorf("%w: manifest size is invalid", ErrInvalidDistributionManifest)
	}
	if err := rejectDuplicateJSONFields(data); err != nil {
		return DistributionManifest{}, "", fmt.Errorf("%w: %v", ErrInvalidDistributionManifest, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var manifest DistributionManifest
	if err := decoder.Decode(&manifest); err != nil {
		return DistributionManifest{}, "", fmt.Errorf("%w: decode: %v", ErrInvalidDistributionManifest, err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return DistributionManifest{}, "", fmt.Errorf("%w: trailing JSON", ErrInvalidDistributionManifest)
	}
	if err := ValidateDistributionManifest(manifest); err != nil {
		return DistributionManifest{}, "", err
	}
	return manifest, digestBytes(data), nil
}

// ValidateDistributionManifest checks the exact Data compiler identities and
// the complete set of supported bounds.
func ValidateDistributionManifest(manifest DistributionManifest) error {
	if manifest.Schema != DistributionSchema || manifest.ModulePath != ModulePath || manifest.CommandImportPath != CommandImportPath || manifest.AnalyzeProtocol != AnalyzeSchema || manifest.EmitProtocol != EmitSchema || manifest.DeclarationLanguage != DeclarationLanguage {
		return fmt.Errorf("%w: identity is incomplete or incompatible", ErrInvalidDistributionManifest)
	}
	if manifest.Limits != (DistributionLimits{
		MaxRoots: MaxRoots, MaxNodes: MaxNodes, MaxImports: MaxImports,
		MaxNesting: MaxNesting, MaxSymbolBytes: MaxSymbolBytes,
		MaxDiagnostics: MaxDiagnostics, MaxDiagnosticBytes: MaxDiagnosticBytes,
		MaxFrameBytes: MaxFrameBytes, MaxSnapshotFiles: MaxSnapshotFiles,
		MaxSnapshotBytes: MaxSnapshotBytes, MaxSourceFileBytes: MaxSourceFileBytes,
		MaxSourcePathBytes: MaxSourcePathBytes, MaxArtifacts: MaxArtifacts,
		MaxArtifactBytes: MaxArtifactBytes, MaxArtifactPathBytes: MaxArtifactPathBytes,
	}) {
		return fmt.Errorf("%w: bounds are incomplete or outside the supported range", ErrInvalidDistributionManifest)
	}
	return nil
}

// DistributionManifestDigest returns the stable digest of the validated,
// canonical manifest. It hashes only manifest identities and bounds.
func DistributionManifestDigest(manifest DistributionManifest) (string, error) {
	if err := ValidateDistributionManifest(manifest); err != nil {
		return "", err
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("%w: marshal: %v", ErrInvalidDistributionManifest, err)
	}
	sum := sha256.Sum256(append([]byte(DistributionSchema+"\x00"), payload...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

// Digest returns the stable digest of the validated manifest.
func (manifest DistributionManifest) Digest() (string, error) {
	return DistributionManifestDigest(manifest)
}

func digestBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func rejectDuplicateJSONFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := scanJSONValue(decoder); err != nil {
		return fmt.Errorf("invalid JSON: %v", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[name]; exists {
				return errors.New("duplicate JSON field")
			}
			seen[name] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim('}') {
			return errors.New("object is not closed")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil {
			return err
		}
		if closing != json.Delim(']') {
			return errors.New("array is not closed")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}
