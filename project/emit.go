package project

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"time"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/compilerselection"
	"github.com/plystra/data/protocol"
	"golang.org/x/mod/modfile"
)

var ErrEmitInput = errors.New("Data emit inputs are invalid or changed")

// EmitAnalysisError preserves validated declaration diagnostics without a plan.
type EmitAnalysisError struct {
	Diagnostics []analyzer.Diagnostic
}

func (e *EmitAnalysisError) Error() string { return "Data declaration analysis did not succeed" }

// EmitPlan contains validated in-memory artifacts, not installed output or a
// migration approval. No target database history has been observed.
type EmitPlan struct {
	Request  protocol.EmitRequest  `json:"request"`
	Response protocol.EmitResponse `json:"response"`
}

// PrepareEmitRequest freezes Data generation inputs, not a Core executable
// application model. Unassigned declarations remain dormant.
func PrepareEmitRequest(modulePath string, analysis protocol.AnalyzeRequest, response protocol.AnalyzeResponse, assignments []Assignment, configured ConfiguredResources) (protocol.EmitRequest, error) {
	if err := protocol.ValidateAnalyzeResponse(analysis, response); err != nil {
		return protocol.EmitRequest{}, ErrCompilerResponse
	}
	if response.Status != protocol.StatusSucceeded {
		return protocol.EmitRequest{}, &EmitAnalysisError{Diagnostics: response.Diagnostics}
	}
	active, err := ValidateActivation(assignments, response.Output.Roots)
	if err != nil {
		return protocol.EmitRequest{}, err
	}
	databases, err := ValidateDatabaseSelections(active, modulePath, configured)
	if err != nil {
		return protocol.EmitRequest{}, err
	}
	selected := make(map[string]DatabaseSelection, len(databases))
	for _, database := range databases {
		selected[database.Resource] = database
	}
	request := protocol.EmitRequest{
		Schema: protocol.EmitSchema, Phase: protocol.EmitPhase, RequestID: analysis.RequestID + "-emit",
		Compiler: analysis.Compiler, Build: analysis.Build, Limits: analysis.Limits,
		Analyze: response.Output, AnalyzeDigest: response.OutputDigest,
		Assignments: make([]protocol.EmitAssignment, 0, len(active)),
	}
	request.Build.BuildTags = append([]string(nil), analysis.Build.BuildTags...)
	sort.Strings(request.Build.BuildTags)
	for _, member := range active {
		database := selected[member.Resource]
		request.Assignments = append(request.Assignments, protocol.EmitAssignment{
			MemberID: member.MemberID, Resource: member.Resource, ResourceContract: databaseResource,
			Provider: database.Provider, Backend: database.Backend, AllowedRoot: "dataaccess/" + member.Resource,
		})
	}
	payload, err := json.Marshal(struct {
		ModulePath    string                    `json:"module_path"`
		SourceDigest  string                    `json:"source_digest"`
		AnalyzeDigest string                    `json:"analyze_digest"`
		Compiler      protocol.CompilerIdentity `json:"compiler"`
		Build         protocol.BuildContext     `json:"build"`
		Members       []ActiveMember            `json:"members"`
		Databases     []DatabaseSelection       `json:"databases"`
		Configured    ConfiguredResources       `json:"configured_resources"`
	}{modulePath, analysis.InputDigest, response.OutputDigest, analysis.Compiler, request.Build, active, databases, configured})
	if err != nil {
		return protocol.EmitRequest{}, ErrEmitInput
	}
	sum := sha256.Sum256(append([]byte("plystra.data.generation-input/v1\x00"), payload...))
	request.FrozenModelDigest = "sha256:" + hex.EncodeToString(sum[:])
	request.InputDigest, err = protocol.EmitInputDigest(request)
	if err != nil || protocol.ValidateEmitRequest(request) != nil {
		return protocol.EmitRequest{}, ErrEmitInput
	}
	return request, nil
}

// PlanCurrentProject analyzes and emits through one exact selected compiler.
// The caller obtains generic configured Resource facts before this call and
// must recheck them before publishing the plan. This function installs nothing.
func PlanCurrentProject(ctx context.Context, projectRoot string, options SourceOptions, configured ConfiguredResources) (EmitPlan, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return EmitPlan{}, ErrEmitInput
	}
	before, err := snapshotModuleInputs(ctx, root, sourceGoEnvironment(options.Offline))
	if err != nil {
		return EmitPlan{}, err
	}
	module, err := modfile.Parse("go.mod", before.GoMod, nil)
	if err != nil || module.Module == nil || module.Module.Mod.Path == "" {
		return EmitPlan{}, ErrEmitInput
	}
	assignmentBytes, err := readSourceFile(filepath.Join(root, "plystra.data.yaml"), MaxAssignmentDocumentBytes)
	if err != nil {
		return EmitPlan{}, ErrEmitInput
	}
	assignments, err := ParseAssignments(assignmentBytes)
	if err != nil {
		return EmitPlan{}, err
	}
	selection, err := compilerselection.Resolve(ctx, root, compilerselection.Options{Offline: options.Offline})
	if err != nil {
		return EmitPlan{}, err
	}
	private, err := os.MkdirTemp("", "plystra-data-plan-")
	if err != nil {
		return EmitPlan{}, ErrCompilerInvocation
	}
	defer os.RemoveAll(private)
	staged, err := StageAnalyzeInput(ctx, root, filepath.Join(private, "snapshot"), options)
	if err != nil {
		return EmitPlan{}, err
	}
	if staged.GoToolchain != selection.GoToolchain {
		return EmitPlan{}, ErrSourceInventory
	}
	identity := protocol.CompilerIdentity{
		ModulePath: protocol.ModulePath, ModuleVersion: selection.ModuleVersion,
		ModuleChecksum: selection.ModuleChecksum, ManifestDigest: selection.ManifestDigest,
		CommandImportPath: selection.Manifest.CommandImportPath,
		AnalyzeProtocol:   selection.Manifest.AnalyzeProtocol, EmitProtocol: selection.Manifest.EmitProtocol,
		DeclarationLanguage: selection.Manifest.DeclarationLanguage, GoToolchain: selection.GoToolchain,
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return EmitPlan{}, ErrCompilerInvocation
	}
	analysis := protocol.AnalyzeRequest{
		Schema: protocol.AnalyzeSchema, Phase: protocol.AnalyzePhase, RequestID: hex.EncodeToString(nonce[:]),
		Compiler: identity, Build: staged.Build, Limits: staged.Manifest.Limits, Snapshot: staged.Manifest,
	}
	analysis.InputDigest, err = protocol.AnalyzeInputDigest(analysis.Snapshot, analysis.Build)
	if err != nil || protocol.ValidateAnalyzeRequest(analysis) != nil {
		return EmitPlan{}, ErrSourceInventory
	}
	binary := filepath.Join(private, "plystra-data-compiler")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := buildSelectedCompiler(ctx, root, binary, selection, options.Offline); err != nil {
		return EmitPlan{}, err
	}
	analyzed, err := invokeSelectedCompiler(ctx, binary, analysis)
	if err != nil {
		return EmitPlan{}, err
	}
	request, err := PrepareEmitRequest(module.Module.Mod.Path, analysis, analyzed, assignments, configured)
	if err != nil {
		return EmitPlan{}, err
	}
	response, err := invokeSelectedEmitter(ctx, binary, request)
	if err != nil {
		return EmitPlan{}, err
	}
	confirmed, err := compilerselection.Resolve(ctx, root, compilerselection.Options{Offline: options.Offline})
	if err != nil || !reflect.DeepEqual(selection, confirmed) {
		return EmitPlan{}, ErrCompilerInvocation
	}
	current, err := StageAnalyzeInput(ctx, root, filepath.Join(private, "snapshot-check"), options)
	if err != nil || !reflect.DeepEqual(staged.Build, current.Build) || staged.GoToolchain != current.GoToolchain {
		return EmitPlan{}, ErrSourceInventory
	}
	firstDigest, firstErr := protocol.SnapshotManifestDigest(staged.Manifest)
	secondDigest, secondErr := protocol.SnapshotManifestDigest(current.Manifest)
	if firstErr != nil || secondErr != nil || firstDigest != secondDigest {
		return EmitPlan{}, ErrSourceInventory
	}
	after, err := snapshotModuleInputs(ctx, root, sourceGoEnvironment(options.Offline))
	currentAssignments, assignmentErr := readSourceFile(filepath.Join(root, "plystra.data.yaml"), MaxAssignmentDocumentBytes)
	if err != nil || assignmentErr != nil || !reflect.DeepEqual(before, after) || !bytes.Equal(assignmentBytes, currentAssignments) {
		return EmitPlan{}, ErrEmitInput
	}
	if err := os.RemoveAll(private); err != nil {
		return EmitPlan{}, ErrCompilerInvocation
	}
	return EmitPlan{Request: request, Response: response}, nil
}

func invokeSelectedEmitter(ctx context.Context, binary string, request protocol.EmitRequest) (protocol.EmitResponse, error) {
	if err := protocol.ValidateEmitRequest(request); err != nil {
		return protocol.EmitResponse{}, ErrEmitInput
	}
	var input bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		return protocol.EmitResponse{}, ErrEmitInput
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(bounded, binary)
	command.WaitDelay = 5 * time.Second
	command.Stdin = &input
	command.Env = compilerEnvironment(request.Compiler.GoToolchain, true)
	stdout, stderr := &boundedOutput{limit: protocol.MaxFrameBytes + 4}, &boundedOutput{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		return protocol.EmitResponse{}, ErrCompilerInvocation
	}
	var response protocol.EmitResponse
	frame := bytes.NewReader(stdout.Bytes())
	if err := protocol.ReadFrame(frame, &response); err != nil || frame.Len() != 0 {
		return protocol.EmitResponse{}, ErrCompilerResponse
	}
	if err := protocol.ValidateEmitResponse(request, response); err != nil {
		return protocol.EmitResponse{}, ErrCompilerResponse
	}
	return response, nil
}
