package project

import (
	"bytes"
	"context"
	"crypto/rand"
	"debug/buildinfo"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"

	"github.com/plystra/data/compilerselection"
	"github.com/plystra/data/protocol"
)

var (
	ErrCompilerInvocation = errors.New("selected Data compiler invocation failed")
	ErrCompilerResponse   = errors.New("selected Data compiler returned an invalid response")
)

type ProjectAnalysis struct {
	Compiler protocol.CompilerIdentity
	Response protocol.AnalyzeResponse
}

// AnalyzeCurrentProject invokes exactly the Project-selected Data compiler
// against a verified private snapshot and installs no Project artifacts.
func AnalyzeCurrentProject(ctx context.Context, projectRoot string, options SourceOptions) (ProjectAnalysis, error) {
	ctx, cancel := context.WithTimeout(ctx, 8*time.Minute)
	defer cancel()
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return ProjectAnalysis{}, ErrSourceInventory
	}
	selection, err := compilerselection.Resolve(ctx, root, compilerselection.Options{Offline: options.Offline})
	if err != nil {
		return ProjectAnalysis{}, err
	}
	private, err := os.MkdirTemp("", "plystra-data-analyze-")
	if err != nil {
		return ProjectAnalysis{}, ErrCompilerInvocation
	}
	defer os.RemoveAll(private)
	staged, err := StageAnalyzeInput(ctx, root, filepath.Join(private, "snapshot"), options)
	if err != nil {
		return ProjectAnalysis{}, err
	}
	if staged.GoToolchain != selection.GoToolchain {
		return ProjectAnalysis{}, ErrSourceInventory
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
		return ProjectAnalysis{}, ErrCompilerInvocation
	}
	request := protocol.AnalyzeRequest{
		Schema: protocol.AnalyzeSchema, Phase: protocol.AnalyzePhase, RequestID: hex.EncodeToString(nonce[:]),
		Compiler: identity, Build: staged.Build, Limits: staged.Manifest.Limits, Snapshot: staged.Manifest,
	}
	request.InputDigest, err = protocol.AnalyzeInputDigest(request.Snapshot, request.Build)
	if err != nil || protocol.ValidateAnalyzeRequest(request) != nil {
		return ProjectAnalysis{}, ErrSourceInventory
	}
	binary := filepath.Join(private, "plystra-data-compiler")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := buildSelectedCompiler(ctx, root, binary, selection, options.Offline); err != nil {
		return ProjectAnalysis{}, err
	}
	response, err := invokeSelectedCompiler(ctx, binary, request)
	if err != nil {
		return ProjectAnalysis{}, err
	}
	if err := protocol.ValidateAnalyzeResponse(request, response); err != nil {
		return ProjectAnalysis{}, ErrCompilerResponse
	}
	confirmed, err := compilerselection.Resolve(ctx, root, compilerselection.Options{Offline: options.Offline})
	if err != nil || !reflect.DeepEqual(selection, confirmed) {
		return ProjectAnalysis{}, ErrCompilerInvocation
	}
	current, err := StageAnalyzeInput(ctx, root, filepath.Join(private, "snapshot-check"), options)
	if err != nil || !reflect.DeepEqual(staged.Build, current.Build) || staged.GoToolchain != current.GoToolchain {
		return ProjectAnalysis{}, ErrSourceInventory
	}
	firstDigest, firstErr := protocol.SnapshotManifestDigest(staged.Manifest)
	secondDigest, secondErr := protocol.SnapshotManifestDigest(current.Manifest)
	if firstErr != nil || secondErr != nil || firstDigest != secondDigest {
		return ProjectAnalysis{}, ErrSourceInventory
	}
	return ProjectAnalysis{Compiler: identity, Response: response}, nil
}

func buildSelectedCompiler(ctx context.Context, projectRoot, binary string, selection compilerselection.Selection, offline bool) error {
	bounded, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	command := exec.CommandContext(bounded, "go", "build", "-mod=readonly", "-buildvcs=false", "-o", binary, protocol.CommandImportPath)
	command.WaitDelay = 5 * time.Second
	command.Dir = projectRoot
	command.Env = compilerEnvironment(selection.GoToolchain, offline)
	stdout, stderr := &boundedOutput{limit: 4096}, &boundedOutput{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil || stdout.Len() != 0 {
		return ErrCompilerInvocation
	}
	info, err := buildinfo.ReadFile(binary)
	if err != nil || info.Path != protocol.CommandImportPath || info.Main.Path != protocol.ModulePath || info.Main.Version != selection.ModuleVersion || info.Main.Sum != selection.ModuleChecksum || info.GoVersion != selection.GoToolchain {
		return ErrCompilerInvocation
	}
	return nil
}

func invokeSelectedCompiler(ctx context.Context, binary string, request protocol.AnalyzeRequest) (protocol.AnalyzeResponse, error) {
	var input bytes.Buffer
	if err := protocol.WriteFrame(&input, request); err != nil {
		return protocol.AnalyzeResponse{}, ErrCompilerInvocation
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
		return protocol.AnalyzeResponse{}, ErrCompilerInvocation
	}
	var response protocol.AnalyzeResponse
	frame := bytes.NewReader(stdout.Bytes())
	if err := protocol.ReadFrame(frame, &response); err != nil || frame.Len() != 0 {
		return protocol.AnalyzeResponse{}, ErrCompilerResponse
	}
	return response, nil
}

func compilerEnvironment(toolchain string, offline bool) []string {
	overrides := map[string]string{
		"GOWORK": "off", "GOFLAGS": "", "GOTOOLCHAIN": toolchain,
		"GOOS": runtime.GOOS, "GOARCH": runtime.GOARCH,
	}
	if offline {
		overrides["GOPROXY"] = "off"
		overrides["GOSUMDB"] = "off"
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if _, overridden := overrides[strings.ToUpper(key)]; !overridden {
			env = append(env, entry)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (output *boundedOutput) Write(data []byte) (int, error) {
	if output.Len()+len(data) > output.limit {
		return 0, io.ErrShortWrite
	}
	return output.Buffer.Write(data)
}
