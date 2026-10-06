package protocol

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
	"github.com/plystra/data/migration"
	"github.com/plystra/data/postgres"
)

const postgresBackend = "postgres/v1"

// EmitAssignment is one already frozen active member and its selected backend.
// The CLI, not the compiler, owns Resource and provider selection.
type EmitAssignment struct {
	MemberID         string `json:"member_id"`
	Resource         string `json:"resource"`
	ResourceContract string `json:"resource_contract"`
	Provider         string `json:"provider"`
	Backend          string `json:"backend"`
	AllowedRoot      string `json:"allowed_root"`
}

// EmitRequest contains only accepted pre-Freeze analysis and frozen inputs.
// No source snapshot or application graph mutation is accepted in this phase.
type EmitRequest struct {
	Schema            string           `json:"schema"`
	Phase             string           `json:"phase"`
	RequestID         string           `json:"request_id"`
	Compiler          CompilerIdentity `json:"compiler"`
	Build             BuildContext     `json:"build"`
	Limits            analyzer.Limits  `json:"limits"`
	InputDigest       string           `json:"input_digest"`
	Analyze           AnalyzeOutput    `json:"analyze"`
	AnalyzeDigest     string           `json:"analyze_digest"`
	FrozenModelDigest string           `json:"frozen_model_digest"`
	Assignments       []EmitAssignment `json:"assignments"`
}

// StagedArtifact is an in-memory, CLI-installable file. The compiler never
// opens AllowedRoot or writes to the Project filesystem.
type StagedArtifact struct {
	Path              string                `json:"path"`
	Mode              uint32                `json:"mode"`
	Bytes             []byte                `json:"bytes"`
	Digest            string                `json:"digest"`
	OwningMembers     []string              `json:"owning_members"`
	Resource          string                `json:"resource"`
	ResourceContract  string                `json:"resource_contract"`
	Provider          string                `json:"provider"`
	Backend           string                `json:"backend"`
	Compiler          CompilerIdentity      `json:"compiler"`
	FrozenModelDigest string                `json:"frozen_model_digest"`
	Migration         *migration.Definition `json:"migration,omitempty"`
}

type manifestEntry struct {
	Path          string   `json:"path"`
	Mode          uint32   `json:"mode"`
	Digest        string   `json:"digest"`
	OwningMembers []string `json:"owning_members"`
}

type EmitOutput struct {
	Artifacts []StagedArtifact `json:"artifacts"`
}

type EmitResponse struct {
	Schema       string                `json:"schema"`
	Phase        string                `json:"phase"`
	RequestID    string                `json:"request_id"`
	InputDigest  string                `json:"input_digest"`
	Status       Status                `json:"status"`
	OutputDigest string                `json:"output_digest,omitempty"`
	Output       EmitOutput            `json:"output"`
	Diagnostics  []analyzer.Diagnostic `json:"diagnostics"`
	Truncated    bool                  `json:"truncated"`
}

// EmitInputDigest binds the accepted analysis, frozen model, assignments,
// compiler distribution, and Go build context without depending on list order.
func EmitInputDigest(request EmitRequest) (string, error) {
	assignments := append([]EmitAssignment(nil), request.Assignments...)
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].MemberID < assignments[j].MemberID })
	build := request.Build
	build.BuildTags = append([]string(nil), build.BuildTags...)
	sort.Strings(build.BuildTags)
	payload, err := json.Marshal(struct {
		Compiler          CompilerIdentity `json:"compiler"`
		Build             BuildContext     `json:"build"`
		Limits            analyzer.Limits  `json:"limits"`
		AnalyzeDigest     string           `json:"analyze_digest"`
		FrozenModelDigest string           `json:"frozen_model_digest"`
		Assignments       []EmitAssignment `json:"assignments"`
	}{request.Compiler, build, request.Limits, request.AnalyzeDigest, request.FrozenModelDigest, assignments})
	if err != nil {
		return "", err
	}
	return digest("plystra.data.emit-input/v1", payload), nil
}

// ValidateEmitRequest rejects changed pre-Freeze data and incomplete frozen
// selections before any PostgreSQL lowering is attempted.
func ValidateEmitRequest(request EmitRequest) error {
	if request.Schema != EmitSchema || request.Phase != EmitPhase || strings.TrimSpace(request.RequestID) == "" {
		return fmt.Errorf("invalid emit schema, phase, or request ID")
	}
	if err := validateCompilerAndBuild(request.Compiler, request.Build); err != nil {
		return err
	}
	if !validDigest(request.FrozenModelDigest) || !validDigest(request.AnalyzeDigest) || !request.Analyze.Valid || request.Analyze.Truncated {
		return fmt.Errorf("accepted analysis and frozen model identity are required")
	}
	computed, err := analyzer.ResultDigest(request.Analyze.Roots, nil, false)
	if err != nil || computed != request.AnalyzeDigest || request.Analyze.Digest != computed {
		return fmt.Errorf("analyze result digest mismatch")
	}
	if len(request.Analyze.Roots) > 1024 || len(request.Assignments) > 1024 {
		return fmt.Errorf("emit member limit exceeded")
	}
	expected, err := EmitInputDigest(request)
	if err != nil || request.InputDigest != expected {
		return fmt.Errorf("emit input digest mismatch")
	}
	return nil
}

// HandleEmit emits one bounded staged artifact set. Rejections contain no
// partial files; the caller must validate the whole response before install.
func HandleEmit(request EmitRequest) EmitResponse {
	response := EmitResponse{Schema: EmitSchema, Phase: EmitPhase, RequestID: request.RequestID, InputDigest: request.InputDigest, Output: EmitOutput{}}
	if err := ValidateEmitRequest(request); err != nil {
		return rejectEmit(response, analyzer.CodeEmitInput, "Frozen Data compiler input is invalid", "Re-run analyze and Freeze, then retry emit with the exact accepted identities.")
	}
	artifacts, code, err := compileEmit(request)
	if err != nil {
		return rejectEmit(response, code, "Selected Data model cannot be emitted", "Correct the active member assignment or unsupported PostgreSQL declaration, then re-run generation.")
	}
	response.Output.Artifacts = artifacts
	payload, err := json.Marshal(artifacts)
	if err != nil {
		return rejectEmit(response, analyzer.CodeEmitArtifact, "Staged Data artifacts are invalid", "Correct the Data declaration and retry generation.")
	}
	response.OutputDigest = digest("plystra.data.emit-output/v1", payload)
	response.Status = StatusSucceeded
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > MaxFrameBytes {
		return rejectEmit(response, analyzer.CodeLimitExceeded, "Staged Data artifact output exceeds the frame limit", "Reduce the selected Data model or split it across Resource instances.")
	}
	if err := ValidateEmitResponse(request, response); err != nil {
		return rejectEmit(response, analyzer.CodeEmitArtifact, "Staged Data artifact validation failed", "Correct the selected Data model and retry generation.")
	}
	return response
}

// ValidateEmitResponse is the pre-install integrity check for an emit result.
// Core implements this wire contract independently rather than importing Data.
func ValidateEmitResponse(request EmitRequest, response EmitResponse) error {
	if err := ValidateEmitRequest(request); err != nil {
		return fmt.Errorf("invalid emit request: %w", err)
	}
	if response.Schema != EmitSchema || response.Phase != EmitPhase || response.RequestID != request.RequestID || response.InputDigest != request.InputDigest || response.Truncated {
		return fmt.Errorf("emit response identity or truncation mismatch")
	}
	if response.Status == StatusRejected {
		if response.OutputDigest != "" || len(response.Output.Artifacts) != 0 || len(response.Diagnostics) == 0 {
			return fmt.Errorf("rejected emit response contains artifacts or no diagnostic")
		}
		return nil
	}
	if response.Status != StatusSucceeded || len(response.Diagnostics) != 0 || !validDigest(response.OutputDigest) {
		return fmt.Errorf("invalid emit response status or digest")
	}
	if len(response.Output.Artifacts) > MaxArtifacts {
		return fmt.Errorf("emit artifact count exceeds limit")
	}
	assignments := make(map[string]EmitAssignment, len(request.Assignments))
	for _, assignment := range request.Assignments {
		assignments[assignment.MemberID] = assignment
	}
	seen := map[string]struct{}{}
	previous := ""
	for _, artifact := range response.Output.Artifacts {
		if len(artifact.Path) > MaxArtifactPathBytes || !safePath(artifact.Path) || len(artifact.Bytes) == 0 || len(artifact.Bytes) > MaxArtifactBytes || artifact.Mode != 0644 || artifact.Digest != postgres.DigestSQL(string(artifact.Bytes)) {
			return fmt.Errorf("invalid staged artifact path, mode, bytes, or digest")
		}
		if previous != "" && artifact.Path <= previous {
			return fmt.Errorf("staged artifacts are not in canonical path order")
		}
		previous = artifact.Path
		key := strings.ToLower(artifact.Path)
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("duplicate or case-colliding artifact path")
		}
		seen[key] = struct{}{}
		if len(artifact.OwningMembers) == 0 {
			return fmt.Errorf("staged artifact has no owning member")
		}
		for index, memberID := range artifact.OwningMembers {
			assignment, exists := assignments[memberID]
			if !exists || assignment.Resource != artifact.Resource || assignment.AllowedRoot == "" || !strings.HasPrefix(artifact.Path, assignment.AllowedRoot+"/") {
				return fmt.Errorf("staged artifact ownership or root mismatch")
			}
			if index > 0 && artifact.OwningMembers[index-1] >= memberID {
				return fmt.Errorf("staged artifact owners are not canonical")
			}
			if assignment.ResourceContract != artifact.ResourceContract || assignment.Provider != artifact.Provider || assignment.Backend != artifact.Backend {
				return fmt.Errorf("staged artifact selected backend mismatch")
			}
		}
		if artifact.Compiler != request.Compiler || artifact.FrozenModelDigest != request.FrozenModelDigest {
			return fmt.Errorf("staged artifact compiler or frozen model mismatch")
		}
		if artifact.Migration != nil {
			definition := artifact.Migration
			compiler := compilerDigest(request.Compiler)
			if len(artifact.OwningMembers) != 1 || definition.Identity.Member != artifact.OwningMembers[0] || definition.ArtifactDigest != artifact.Digest || definition.Backend != artifact.Backend || definition.Compiler != compiler {
				return fmt.Errorf("staged migration provenance mismatch")
			}
		}
	}
	if err := validateManifests(request, response.Output.Artifacts); err != nil {
		return err
	}
	payload, err := json.Marshal(response.Output.Artifacts)
	if err != nil || response.OutputDigest != digest("plystra.data.emit-output/v1", payload) {
		return fmt.Errorf("emit output digest mismatch")
	}
	encoded, err := json.Marshal(response)
	if err != nil || len(encoded) > MaxFrameBytes {
		return fmt.Errorf("emit output frame exceeds limit")
	}
	return nil
}

func validateManifests(request EmitRequest, artifacts []StagedArtifact) error {
	type instanceManifest struct {
		Schema            string            `json:"schema"`
		Resource          string            `json:"resource"`
		ResourceContract  string            `json:"resource_contract"`
		Provider          string            `json:"provider"`
		Backend           string            `json:"backend"`
		Compiler          CompilerIdentity  `json:"compiler"`
		AnalyzeDigest     string            `json:"analyze_digest"`
		InputDigest       string            `json:"input_digest"`
		FrozenModelDigest string            `json:"frozen_model_digest"`
		MemberModels      map[string]string `json:"member_models"`
		Artifacts         []manifestEntry   `json:"artifacts"`
	}
	byResource := map[string][]StagedArtifact{}
	for _, artifact := range artifacts {
		byResource[artifact.Resource] = append(byResource[artifact.Resource], artifact)
	}
	for _, assignment := range request.Assignments {
		if len(byResource[assignment.Resource]) == 0 {
			return fmt.Errorf("selected Resource %s has no staged artifacts", assignment.Resource)
		}
	}
	roots := make(map[string]analyzer.Root, len(request.Analyze.Roots))
	for _, root := range request.Analyze.Roots {
		roots[root.MemberID] = root
	}
	expected := map[string]map[string]struct{}{}
	owners := map[string][]string{}
	for _, assignment := range request.Assignments {
		resource := assignment.Resource
		if expected[resource] == nil {
			expected[resource] = map[string]struct{}{
				"generated/data/" + resource + "/manifest.json":     {},
				"generated/data/" + resource + "/schema/schema.sql": {},
			}
		}
		owners[resource] = append(owners[resource], assignment.MemberID)
		for _, query := range roots[assignment.MemberID].Model.Queries {
			expected[resource]["generated/data/"+resource+"/queries/"+assignment.MemberID+"/"+query.ID+".sql"] = struct{}{}
		}
		for _, item := range roots[assignment.MemberID].Model.Migrations {
			expected[resource]["generated/data/"+resource+"/migrations/"+assignment.MemberID+"/"+item.ID+".sql"] = struct{}{}
		}
	}
	for resource, instanceArtifacts := range byResource {
		sort.Strings(owners[resource])
		if len(instanceArtifacts) != len(expected[resource]) {
			return fmt.Errorf("Resource %s staged artifact inventory is incomplete", resource)
		}
		var manifestArtifact *StagedArtifact
		var schemaArtifact *StagedArtifact
		entries := make([]manifestEntry, 0, len(instanceArtifacts))
		for index := range instanceArtifacts {
			artifact := &instanceArtifacts[index]
			if _, allowed := expected[resource][artifact.Path]; !allowed {
				return fmt.Errorf("Resource %s has unexpected staged artifact", resource)
			}
			if artifact.Path == "generated/data/"+resource+"/manifest.json" {
				manifestArtifact = artifact
				continue
			}
			if artifact.Path == "generated/data/"+resource+"/schema/schema.sql" {
				schemaArtifact = artifact
			}
			entries = append(entries, manifestEntry{Path: artifact.Path, Mode: artifact.Mode, Digest: artifact.Digest, OwningMembers: artifact.OwningMembers})
		}
		if manifestArtifact == nil || schemaArtifact == nil || !reflect.DeepEqual(manifestArtifact.OwningMembers, owners[resource]) || !reflect.DeepEqual(schemaArtifact.OwningMembers, owners[resource]) || manifestArtifact.Migration != nil || schemaArtifact.Migration != nil {
			return fmt.Errorf("Resource %s has invalid schema or manifest ownership", resource)
		}
		var manifest instanceManifest
		if err := decodeRequest(manifestArtifact.Bytes, &manifest); err != nil {
			return fmt.Errorf("invalid staged manifest: %w", err)
		}
		if manifest.Schema != "plystra.data-instance-manifest/v1" || manifest.Resource != resource || manifest.ResourceContract != manifestArtifact.ResourceContract || manifest.Provider != manifestArtifact.Provider || manifest.Backend != manifestArtifact.Backend || manifest.Compiler != request.Compiler || manifest.AnalyzeDigest != request.AnalyzeDigest || manifest.InputDigest != request.InputDigest || manifest.FrozenModelDigest != request.FrozenModelDigest || !reflect.DeepEqual(manifest.Artifacts, entries) {
			return fmt.Errorf("staged manifest provenance or artifact inventory mismatch")
		}
		if len(manifest.MemberModels) != len(manifestArtifact.OwningMembers) {
			return fmt.Errorf("staged manifest member inventory mismatch")
		}
		for _, memberID := range manifestArtifact.OwningMembers {
			root, exists := roots[memberID]
			if !exists {
				return fmt.Errorf("staged manifest has unknown member")
			}
			modelDigest, err := declaration.DigestModel(root.Model)
			if err != nil || manifest.MemberModels[memberID] != modelDigest {
				return fmt.Errorf("staged manifest logical model digest mismatch")
			}
		}
	}
	return nil
}

func compileEmit(request EmitRequest) ([]StagedArtifact, analyzer.DiagnosticCode, error) {
	byRoot := make(map[string]analyzer.Root, len(request.Analyze.Roots))
	for _, root := range request.Analyze.Roots {
		if root.MemberID == "" {
			return nil, analyzer.CodeEmitInput, fmt.Errorf("empty member ID")
		}
		if _, exists := byRoot[root.MemberID]; exists {
			return nil, analyzer.CodeEmitInput, fmt.Errorf("duplicate member ID")
		}
		model, err := (declaration.Member[struct{}]{Namespace: root.Model.Namespace, Imports: root.Model.Imports, Tables: root.Model.Tables, Queries: root.Model.Queries, Plans: root.Model.Plans, Migrations: root.Model.Migrations}).Normalize()
		if err != nil {
			return nil, analyzer.CodeEmitInput, fmt.Errorf("invalid member model: %w", err)
		}
		normalizedJSON, err := json.Marshal(model)
		if err != nil {
			return nil, analyzer.CodeEmitInput, err
		}
		inputJSON, err := json.Marshal(root.Model)
		if err != nil || !bytes.Equal(normalizedJSON, inputJSON) {
			return nil, analyzer.CodeEmitInput, fmt.Errorf("unnormalized member model")
		}
		byRoot[root.MemberID] = root
	}
	byMember := make(map[string]EmitAssignment, len(request.Assignments))
	type group struct {
		assignment EmitAssignment
		members    []postgres.SchemaMember
		memberIDs  []string
	}
	groups := map[string]*group{}
	for _, assignment := range request.Assignments {
		root, exists := byRoot[assignment.MemberID]
		if !exists || byMember[assignment.MemberID].MemberID != "" {
			return nil, analyzer.CodeEmitInput, fmt.Errorf("missing or duplicate active member")
		}
		if !safeInstance(assignment.Resource) || assignment.ResourceContract != "data.database/v1" || assignment.Provider == "" || assignment.Backend != postgresBackend {
			return nil, analyzer.CodeEmitBackend, fmt.Errorf("invalid selected backend")
		}
		if assignment.AllowedRoot != "generated/data/"+assignment.Resource || !safePath(assignment.AllowedRoot) {
			return nil, analyzer.CodeEmitPath, fmt.Errorf("invalid allowed root")
		}
		byMember[assignment.MemberID] = assignment
		item := groups[assignment.Resource]
		if item == nil {
			item = &group{assignment: assignment}
			groups[assignment.Resource] = item
		} else if item.assignment.ResourceContract != assignment.ResourceContract || item.assignment.Provider != assignment.Provider || item.assignment.Backend != assignment.Backend || item.assignment.AllowedRoot != assignment.AllowedRoot {
			return nil, analyzer.CodeEmitBackend, fmt.Errorf("inconsistent Resource selection")
		}
		item.members = append(item.members, postgres.SchemaMember{MemberID: root.MemberID, Resource: assignment.Resource, Model: root.Model})
		item.memberIDs = append(item.memberIDs, root.MemberID)
	}
	for memberID := range byMember {
		for _, imported := range byRoot[memberID].Model.Imports {
			other, active := byMember[imported]
			if !active || other.Resource != byMember[memberID].Resource {
				return nil, analyzer.CodeEmitBackend, fmt.Errorf("imported member is inactive or assigned to another Resource")
			}
		}
	}
	resources := make([]string, 0, len(groups))
	for resource := range groups {
		resources = append(resources, resource)
	}
	sort.Strings(resources)
	var artifacts []StagedArtifact
	compiler := compilerDigest(request.Compiler)
	for _, resource := range resources {
		item := groups[resource]
		sort.Strings(item.memberIDs)
		start := len(artifacts)
		schema, err := postgres.CompileSchema(item.members)
		if err != nil {
			return nil, analyzer.CodeEmitBackend, err
		}
		artifacts = append(artifacts, stagedSQL(item.assignment, item.memberIDs, request, schema.Path, schema.SQL, nil))
		queries, err := postgres.CompileQueries(item.members)
		if err != nil {
			return nil, analyzer.CodeEmitBackend, err
		}
		for _, query := range queries {
			artifacts = append(artifacts, stagedSQL(item.assignment, []string{query.MemberID}, request, query.Path, query.SQL, nil))
		}
		migrations, err := postgres.CompileMigrations(item.members, compiler)
		if err != nil {
			return nil, analyzer.CodeEmitBackend, err
		}
		for _, migrationArtifact := range migrations {
			definition := migrationArtifact.Definition
			relative := "migrations/" + definition.Identity.Member + "/" + definition.Identity.Name + ".sql"
			artifacts = append(artifacts, stagedSQL(item.assignment, []string{definition.Identity.Member}, request, relative, migrationArtifact.SQL, &definition))
		}
		entries := make([]manifestEntry, 0, len(artifacts)-start)
		for _, artifact := range artifacts[start:] {
			entries = append(entries, manifestEntry{Path: artifact.Path, Mode: artifact.Mode, Digest: artifact.Digest, OwningMembers: artifact.OwningMembers})
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
		modelDigests := make(map[string]string, len(item.members))
		for _, member := range item.members {
			modelDigest, err := declaration.DigestModel(member.Model)
			if err != nil {
				return nil, analyzer.CodeEmitInput, err
			}
			modelDigests[member.MemberID] = modelDigest
		}
		manifest, err := json.Marshal(struct {
			Schema            string            `json:"schema"`
			Resource          string            `json:"resource"`
			ResourceContract  string            `json:"resource_contract"`
			Provider          string            `json:"provider"`
			Backend           string            `json:"backend"`
			Compiler          CompilerIdentity  `json:"compiler"`
			AnalyzeDigest     string            `json:"analyze_digest"`
			InputDigest       string            `json:"input_digest"`
			FrozenModelDigest string            `json:"frozen_model_digest"`
			MemberModels      map[string]string `json:"member_models"`
			Artifacts         []manifestEntry   `json:"artifacts"`
		}{"plystra.data-instance-manifest/v1", resource, item.assignment.ResourceContract, item.assignment.Provider, item.assignment.Backend, request.Compiler, request.AnalyzeDigest, request.InputDigest, request.FrozenModelDigest, modelDigests, entries})
		if err != nil {
			return nil, analyzer.CodeEmitArtifact, err
		}
		artifacts = append(artifacts, StagedArtifact{
			Path: path.Join(item.assignment.AllowedRoot, "manifest.json"), Mode: 0644, Bytes: manifest,
			Digest: postgres.DigestSQL(string(manifest)), OwningMembers: append([]string(nil), item.memberIDs...),
			Resource: resource, ResourceContract: item.assignment.ResourceContract, Provider: item.assignment.Provider,
			Backend: item.assignment.Backend, Compiler: request.Compiler, FrozenModelDigest: request.FrozenModelDigest,
		})
	}
	sort.Slice(artifacts, func(i, j int) bool { return artifacts[i].Path < artifacts[j].Path })
	seen := map[string]struct{}{}
	for _, artifact := range artifacts {
		if !safePath(artifact.Path) || len(artifact.Path) > MaxArtifactPathBytes || !strings.HasPrefix(artifact.Path, "generated/data/"+artifact.Resource+"/") {
			return nil, analyzer.CodeEmitPath, fmt.Errorf("artifact escapes allowed root")
		}
		key := strings.ToLower(artifact.Path)
		if _, duplicate := seen[key]; duplicate {
			return nil, analyzer.CodeEmitArtifact, fmt.Errorf("duplicate artifact path")
		}
		seen[key] = struct{}{}
		if len(artifact.Bytes) > MaxArtifactBytes {
			return nil, analyzer.CodeLimitExceeded, fmt.Errorf("artifact byte limit exceeded")
		}
	}
	if len(artifacts) > MaxArtifacts {
		return nil, analyzer.CodeLimitExceeded, fmt.Errorf("artifact count limit exceeded")
	}
	return artifacts, "", nil
}

func stagedSQL(assignment EmitAssignment, members []string, request EmitRequest, relative, sql string, definition *migration.Definition) StagedArtifact {
	content := []byte(sql)
	return StagedArtifact{
		Path: path.Join(assignment.AllowedRoot, relative), Mode: 0644, Bytes: content,
		Digest: postgres.DigestSQL(sql), OwningMembers: append([]string(nil), members...),
		Resource: assignment.Resource, ResourceContract: assignment.ResourceContract,
		Provider: assignment.Provider, Backend: assignment.Backend, Compiler: request.Compiler,
		FrozenModelDigest: request.FrozenModelDigest, Migration: definition,
	}
}

func rejectEmit(response EmitResponse, code analyzer.DiagnosticCode, message, action string) EmitResponse {
	response.Status = StatusRejected
	response.Output = EmitOutput{}
	response.OutputDigest = ""
	response.Diagnostics = []analyzer.Diagnostic{{Code: code, Message: message, Action: action}}
	return response
}

func safeInstance(value string) bool {
	if value == "" || len(value) > 128 || value[0] < 'a' || value[0] > 'z' || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value[1:] {
		if character != '.' && character != '-' && character != '_' && (character < 'a' || character > 'z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func safePath(value string) bool {
	if value == "" || strings.ContainsAny(value, "\\:\x00") || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
	}
	return true
}

func digest(domain string, payload []byte) string {
	sum := sha256.Sum256(append([]byte(domain+"\x00"), payload...))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func compilerDigest(identity CompilerIdentity) string {
	payload, _ := json.Marshal(identity)
	return digest("plystra.data.compiler-identity/v1", payload)
}
