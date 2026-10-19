package project

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/protocol"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/sumdb/dirhash"
)

var ErrSourceInventory = errors.New("Data Project source inventory is unavailable")

const (
	maxGoOutput = 8 << 20
	goTimeout   = 45 * time.Second
)

type SourceOptions struct {
	Offline   bool
	BuildTags []string
}

type SourceInventory struct {
	Packages         []SourcePackage
	StandardPackages []string
	Build            protocol.BuildContext
}

type ModuleSource struct {
	Path        string `json:"path"`
	Version     string `json:"version,omitempty"`
	Replacement string `json:"replacement,omitempty"`
	Origin      string `json:"origin"`
	Checksum    string `json:"checksum,omitempty"`
}

type SourcePackage struct {
	analyzer.Package
	Module ModuleSource
}

type listedModule struct {
	Path    string        `json:"Path"`
	Version string        `json:"Version"`
	Sum     string        `json:"Sum"`
	Dir     string        `json:"Dir"`
	Main    bool          `json:"Main"`
	Error   any           `json:"Error"`
	Replace *listedModule `json:"Replace"`
}

type listedPackage struct {
	Dir        string   `json:"Dir"`
	ImportPath string   `json:"ImportPath"`
	GoFiles    []string `json:"GoFiles"`
	CgoFiles   []string `json:"CgoFiles"`
}

type packageCandidate struct {
	moduleRoot string
	directory  string
	importPath string
	module     ModuleSource
}

// DiscoverDataRoots records Go-selected authored files in packages with a
// Data declaration directive across the current and dependency Projects.
// It does not require a successful package build or generated access output.
func DiscoverDataRoots(ctx context.Context, projectRoot string, options SourceOptions) (SourceInventory, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil || !regularFile(filepath.Join(root, "go.mod")) || !regularFile(filepath.Join(root, "plystra.yaml")) {
		return SourceInventory{}, ErrSourceInventory
	}
	goMod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || len(goMod) > maxGoOutput {
		return SourceInventory{}, ErrSourceInventory
	}
	goSum, err := readOptionalInput(filepath.Join(root, "go.sum"))
	if err != nil {
		return SourceInventory{}, err
	}
	parsedMod, err := modfile.Parse("go.mod", goMod, nil)
	if err != nil || parsedMod.Module == nil {
		return SourceInventory{}, ErrSourceInventory
	}
	applicationPath := parsedMod.Module.Mod.Path
	env := sourceGoEnvironment(options.Offline)
	flagsOutput, err := sourceGoOutput(ctx, root, env, "env", "GOFLAGS")
	if err != nil || unsupportedSourceGoFlags(strings.TrimSpace(string(flagsOutput))) {
		return SourceInventory{}, ErrSourceInventory
	}
	selectedTags := options.BuildTags
	if len(selectedTags) == 0 {
		selectedTags = goFlagsBuildTags(string(flagsOutput))
	}
	build, err := selectedBuildContext(ctx, root, env, selectedTags)
	if err != nil {
		return SourceInventory{}, err
	}
	output, err := sourceGoOutput(ctx, root, env, "list", "-m", "-json", "-mod=readonly", "all")
	if err != nil {
		return SourceInventory{}, ErrSourceInventory
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	var modules []listedModule
	for {
		var item listedModule
		if err := decoder.Decode(&item); errors.Is(err, io.EOF) {
			break
		} else if err != nil || item.Path == "" || item.Error != nil {
			return SourceInventory{}, ErrSourceInventory
		}
		modules = append(modules, item)
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].Path < modules[j].Path })
	var candidates []packageCandidate
	var verified []struct{ root, version, checksum, modulePath string }
	for _, module := range modules {
		moduleRoot, source, err := selectedModuleRoot(ctx, root, env, module)
		if err != nil {
			return SourceInventory{}, err
		}
		if !regularFile(filepath.Join(moduleRoot, "plystra.yaml")) {
			continue
		}
		if source.Checksum != "" {
			selectedPath, selectedVersion := module.Path, module.Version
			if module.Replace != nil {
				selectedPath, selectedVersion = module.Replace.Path, module.Replace.Version
			}
			verified = append(verified, struct{ root, version, checksum, modulePath string }{moduleRoot, selectedVersion, source.Checksum, selectedPath})
		}
		found, err := probeDataPackages(module.Path, moduleRoot, applicationPath, source)
		if err != nil {
			return SourceInventory{}, err
		}
		candidates = append(candidates, found...)
	}
	var result SourceInventory
	result.Build = build
	roots := make(map[string]string, len(candidates))
	for _, candidate := range candidates {
		pkg, selected, err := loadCandidate(ctx, root, candidate, env, options.BuildTags)
		if err != nil {
			return SourceInventory{}, err
		}
		if selected {
			result.Packages = append(result.Packages, SourcePackage{Package: pkg, Module: candidate.module})
			roots[pkg.ImportPath] = candidate.moduleRoot
		}
	}
	sort.Slice(result.Packages, func(i, j int) bool { return result.Packages[i].ImportPath < result.Packages[j].ImportPath })
	if err := validateSourcePackages(result.Packages, roots); err != nil {
		return SourceInventory{}, err
	}
	for _, source := range verified {
		if err := verifyModuleSource(source.root, source.modulePath, source.version, source.checksum); err != nil {
			return SourceInventory{}, err
		}
	}
	currentMod, modErr := os.ReadFile(filepath.Join(root, "go.mod"))
	currentSum, sumErr := readOptionalInput(filepath.Join(root, "go.sum"))
	if modErr != nil || sumErr != nil || !bytes.Equal(currentMod, goMod) || !bytes.Equal(currentSum, goSum) {
		return SourceInventory{}, ErrSourceInventory
	}
	return result, nil
}

func validateSourcePackages(selected []SourcePackage, roots map[string]string) error {
	packages := make([]analyzer.Package, len(selected))
	count, total := 0, 0
	for i := range selected {
		packages[i] = selected[i].Package
		for _, file := range packages[i].Files {
			count++
			total += len(file.Content)
			if count > protocol.MaxSnapshotFiles || total > protocol.MaxSnapshotBytes || len(file.Path) > protocol.MaxSourcePathBytes {
				return ErrSourceInventory
			}
			current, err := readSourceFile(filepath.Join(roots[packages[i].ImportPath], filepath.FromSlash(file.Path)), protocol.MaxSourceFileBytes)
			if err != nil || !bytes.Equal(current, file.Content) {
				return ErrSourceInventory
			}
		}
	}
	if _, err := analyzer.SnapshotDigest(analyzer.Snapshot{Packages: packages}); err != nil {
		return fmt.Errorf("%w: invalid selected package inventory", ErrSourceInventory)
	}
	return nil
}

func readOptionalInput(name string) ([]byte, error) {
	data, err := os.ReadFile(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil || len(data) > maxGoOutput {
		return nil, ErrSourceInventory
	}
	return data, nil
}

func selectedModuleRoot(ctx context.Context, projectRoot string, env []string, module listedModule) (string, ModuleSource, error) {
	source := ModuleSource{Path: module.Path, Version: module.Version}
	selected := module
	if module.Main {
		source.Origin = "workspace"
		if module.Path == modulePath(projectRoot) {
			source.Origin = "current"
		}
		root, err := canonicalModuleRoot(module.Dir)
		if err != nil || !validModuleIdentity(root, module.Path) {
			return "", ModuleSource{}, ErrSourceInventory
		}
		return root, source, nil
	}
	if module.Replace != nil {
		selected = *module.Replace
		source.Replacement = selected.Path
		if selected.Version != "" {
			source.Replacement += "@" + selected.Version
		} else {
			source.Origin = "local-replacement"
			root, err := canonicalModuleRoot(selected.Dir)
			if err != nil || !validModuleIdentity(root, module.Path) {
				return "", ModuleSource{}, ErrSourceInventory
			}
			return root, source, nil
		}
	}
	source.Origin = "module"
	if module.Replace != nil {
		source.Origin = "versioned-replacement"
	}
	if selected.Path == "" || selected.Version == "" {
		return "", ModuleSource{}, ErrSourceInventory
	}
	isolated, err := os.MkdirTemp("", "plystra-data-module-")
	if err != nil {
		return "", ModuleSource{}, ErrSourceInventory
	}
	defer os.RemoveAll(isolated)
	output, err := sourceGoOutput(ctx, isolated, env, "mod", "download", "-json", selected.Path+"@"+selected.Version)
	if err != nil {
		return "", ModuleSource{}, ErrSourceInventory
	}
	var downloaded struct {
		Path    string `json:"Path"`
		Version string `json:"Version"`
		Dir     string `json:"Dir"`
		Sum     string `json:"Sum"`
	}
	if err := json.Unmarshal(output, &downloaded); err != nil || downloaded.Path != selected.Path || downloaded.Version != selected.Version || downloaded.Dir == "" {
		return "", ModuleSource{}, ErrSourceInventory
	}
	root, err := canonicalModuleRoot(downloaded.Dir)
	if err != nil || module.Dir != "" && !sameSourceDirectory(root, module.Dir) ||
		module.Replace != nil && selected.Dir != "" && !sameSourceDirectory(root, selected.Dir) {
		return "", ModuleSource{}, ErrSourceInventory
	}
	if !regularFile(filepath.Join(root, "plystra.yaml")) {
		return root, source, nil
	}
	checksum, err := selectedChecksum(ctx, projectRoot, env, selected.Path, selected.Version)
	if err != nil || downloaded.Sum != checksum ||
		(module.Replace == nil && module.Sum != "" && module.Sum != checksum) ||
		(module.Replace != nil && selected.Sum != "" && selected.Sum != checksum) {
		return "", ModuleSource{}, ErrSourceInventory
	}
	source.Checksum = checksum
	if err := verifyModuleSource(root, selected.Path, selected.Version, checksum); err != nil {
		return "", ModuleSource{}, err
	}
	return root, source, nil
}

func modulePath(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return ""
	}
	parsed, err := modfile.Parse("go.mod", data, nil)
	if err != nil || parsed.Module == nil {
		return ""
	}
	return parsed.Module.Mod.Path
}

func validModuleIdentity(root, expected string) bool {
	return modulePath(root) == expected
}

func selectedChecksum(ctx context.Context, projectRoot string, env []string, modulePath, version string) (string, error) {
	files := []string{filepath.Join(projectRoot, "go.sum")}
	output, err := sourceGoOutput(ctx, projectRoot, env, "env", "GOWORK")
	if err != nil {
		return "", ErrSourceInventory
	}
	if work := strings.TrimSpace(string(output)); work != "" && work != "off" {
		files = append(files, filepath.Join(filepath.Dir(work), "go.work.sum"))
	}
	var checksum string
	for _, file := range files {
		data, err := os.ReadFile(file)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || len(data) > maxGoOutput {
			return "", ErrSourceInventory
		}
		for _, line := range bytes.Split(data, []byte{'\n'}) {
			parts := bytes.Fields(line)
			if len(parts) != 3 || string(parts[0]) != modulePath || string(parts[1]) != version {
				continue
			}
			value := string(parts[2])
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "h1:"))
			if !strings.HasPrefix(value, "h1:") || err != nil || len(decoded) != sha256.Size || checksum != "" && checksum != value {
				return "", ErrSourceInventory
			}
			checksum = value
		}
	}
	if checksum == "" {
		return "", ErrSourceInventory
	}
	return checksum, nil
}

func verifyModuleSource(root, modulePath, version, checksum string) error {
	if !validModuleIdentity(root, modulePath) {
		return ErrSourceInventory
	}
	if err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil || !entry.IsDir() && !entry.Type().IsRegular() {
			return ErrSourceInventory
		}
		return nil
	}); err != nil {
		return ErrSourceInventory
	}
	actual, err := dirhash.HashDir(root, modulePath+"@"+version, dirhash.Hash1)
	if err != nil || actual != checksum {
		return ErrSourceInventory
	}
	return nil
}

func canonicalModuleRoot(root string) (string, error) {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(canonical) {
		return "", ErrSourceInventory
	}
	info, err := os.Stat(canonical)
	if err != nil || !info.IsDir() {
		return "", ErrSourceInventory
	}
	return filepath.Clean(canonical), nil
}

func probeDataPackages(modulePath, moduleRoot, applicationPath string, source ModuleSource) ([]packageCandidate, error) {
	var candidates []packageCandidate
	err := filepath.WalkDir(moduleRoot, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if entry.IsDir() {
			if current == moduleRoot {
				return nil
			}
			name := entry.Name()
			if strings.HasPrefix(name, ".") || name == "generated" || name == "dataaccess" || name == "vendor" || name == "testdata" || name == "fixtures" || name == "dist" {
				return filepath.SkipDir
			}
			if regularFile(filepath.Join(current, "go.mod")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		candidate, err := hasCandidateHint(current)
		if err != nil {
			return err
		}
		if !candidate {
			return nil
		}
		directory := filepath.Dir(current)
		relative, err := filepath.Rel(moduleRoot, directory)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return ErrSourceInventory
		}
		importPath := modulePath
		if relative != "." {
			importPath = path.Join(modulePath, filepath.ToSlash(relative))
		}
		if !importableFrom(applicationPath, importPath) {
			return nil
		}
		candidates = append(candidates, packageCandidate{moduleRoot: moduleRoot, directory: directory, importPath: importPath, module: source})
		return nil
	})
	if err != nil {
		return nil, ErrSourceInventory
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].importPath < candidates[j].importPath })
	unique := candidates[:0]
	for _, candidate := range candidates {
		if len(unique) == 0 || unique[len(unique)-1].importPath != candidate.importPath {
			unique = append(unique, candidate)
		}
	}
	return unique, nil
}

func hasCandidateHint(name string) (bool, error) {
	opened, err := os.Open(name)
	if err != nil {
		return false, ErrSourceInventory
	}
	defer opened.Close()
	reader := bufio.NewReader(opened)
	const keep = len("//plystra:data") - 1
	var tail []byte
	buffer := make([]byte, 32<<10)
	for {
		count, err := reader.Read(buffer)
		combined := append(tail, buffer[:count]...)
		if bytes.Contains(combined, []byte("//plystra:data")) || bytes.Contains(combined, []byte("/dataaccess")) {
			return true, nil
		}
		if len(combined) > keep {
			tail = append(tail[:0], combined[len(combined)-keep:]...)
		} else {
			tail = append(tail[:0], combined...)
		}
		if errors.Is(err, io.EOF) {
			return false, nil
		}
		if err != nil {
			return false, ErrSourceInventory
		}
	}
}

func importableFrom(applicationPath, importPath string) bool {
	parts := strings.Split(importPath, "/")
	for index, part := range parts {
		if part != "internal" {
			continue
		}
		parent := strings.Join(parts[:index], "/")
		if applicationPath != parent && !strings.HasPrefix(applicationPath, parent+"/") {
			return false
		}
	}
	return true
}

func loadCandidate(ctx context.Context, projectRoot string, candidate packageCandidate, env []string, tags []string) (analyzer.Package, bool, error) {
	args := []string{"list", "-e", "-json", "-mod=readonly"}
	if len(tags) != 0 {
		args = append(args, "-tags="+strings.Join(tags, ","))
	}
	args = append(args, candidate.importPath)
	output, err := sourceGoOutput(ctx, projectRoot, env, args...)
	if err != nil {
		return analyzer.Package{}, false, ErrSourceInventory
	}
	var listed listedPackage
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(&listed); err != nil || listed.ImportPath != candidate.importPath || !sameSourceDirectory(candidate.directory, listed.Dir) {
		return analyzer.Package{}, false, ErrSourceInventory
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return analyzer.Package{}, false, ErrSourceInventory
	}
	if len(listed.GoFiles)+len(listed.CgoFiles) == 0 {
		return analyzer.Package{}, false, nil
	}
	files := append(append([]string(nil), listed.GoFiles...), listed.CgoFiles...)
	sort.Strings(files)
	pkg := analyzer.Package{ImportPath: listed.ImportPath, RootEligibility: analyzer.RootEligible}
	selectedDirective := false
	for index, name := range files {
		if name == "" || filepath.Base(name) != name || !strings.HasSuffix(name, ".go") ||
			(index > 0 && name == files[index-1]) {
			return analyzer.Package{}, false, ErrSourceInventory
		}
		absolute := filepath.Join(candidate.directory, name)
		content, err := readSourceFile(absolute, protocol.MaxSourceFileBytes)
		if err != nil {
			return analyzer.Package{}, false, ErrSourceInventory
		}
		selectedDirective = selectedDirective || hasDataDirective(name, content)
		relative, err := filepath.Rel(candidate.moduleRoot, absolute)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return analyzer.Package{}, false, ErrSourceInventory
		}
		pkg.Files = append(pkg.Files, analyzer.SourceFile{Path: filepath.ToSlash(relative), Content: content})
	}
	for _, file := range pkg.Files {
		if importsGeneratedAccess(file.Path, file.Content) {
			return analyzer.Package{}, false, fmt.Errorf("%w: authored package imports dataaccess", ErrSourceInventory)
		}
	}
	if !selectedDirective {
		return pkg, false, nil
	}
	return pkg, true, nil
}

func hasDataDirective(name string, content []byte) bool {
	parsed, _ := parser.ParseFile(token.NewFileSet(), name, content, parser.ParseComments)
	if parsed == nil {
		return bytes.Contains(content, []byte("//plystra:data"))
	}
	for _, group := range parsed.Comments {
		for _, comment := range group.List {
			if comment.Text == "//plystra:data" || strings.HasPrefix(comment.Text, "//plystra:data ") {
				return true
			}
		}
	}
	return false
}

func importsGeneratedAccess(name string, content []byte) bool {
	parsed, _ := parser.ParseFile(token.NewFileSet(), name, content, parser.ImportsOnly)
	if parsed == nil {
		return false
	}
	for _, imported := range parsed.Imports {
		value, err := strconv.Unquote(imported.Path.Value)
		if err == nil && (strings.HasSuffix(value, "/dataaccess") || strings.Contains(value, "/dataaccess/")) {
			return true
		}
	}
	return false
}

func selectedBuildContext(ctx context.Context, root string, env []string, tags []string) (protocol.BuildContext, error) {
	output, err := sourceGoOutput(ctx, root, env, "env", "GOOS", "GOARCH")
	if err != nil {
		return protocol.BuildContext{}, ErrSourceInventory
	}
	fields := strings.Fields(string(output))
	if len(fields) != 2 || fields[0] == "" || fields[1] == "" {
		return protocol.BuildContext{}, ErrSourceInventory
	}
	selected := append([]string(nil), tags...)
	sort.Strings(selected)
	for index, tag := range selected {
		if tag == "" || strings.ContainsAny(tag, ", \t\n") || (index > 0 && tag == selected[index-1]) {
			return protocol.BuildContext{}, ErrSourceInventory
		}
	}
	return protocol.BuildContext{GOOS: fields[0], GOARCH: fields[1], BuildTags: selected}, nil
}

func readSourceFile(path string, maximum int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > int64(maximum) {
		return nil, ErrSourceInventory
	}
	opened, err := os.Open(path)
	if err != nil {
		return nil, ErrSourceInventory
	}
	defer opened.Close()
	content, err := io.ReadAll(io.LimitReader(opened, int64(maximum)+1))
	if err != nil || len(content) > maximum || int64(len(content)) != info.Size() {
		return nil, ErrSourceInventory
	}
	return content, nil
}

func regularFile(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular()
}

func sameSourceDirectory(left, right string) bool {
	a, errA := filepath.EvalSymlinks(left)
	b, errB := filepath.EvalSymlinks(right)
	return errA == nil && errB == nil && filepath.Clean(a) == filepath.Clean(b)
}

func sourceGoEnvironment(offline bool) []string {
	overrides := map[string]string{}
	if offline {
		overrides["GOPROXY"] = "off"
		overrides["GOSUMDB"] = "off"
	}
	result := make([]string, 0, len(os.Environ())+len(overrides))
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if _, exists := overrides[strings.ToUpper(name)]; !exists {
			result = append(result, entry)
		}
	}
	for name, value := range overrides {
		result = append(result, name+"="+value)
	}
	return result
}

func goFlagsBuildTags(flags string) []string {
	var tags []string
	for _, field := range strings.Fields(flags) {
		flag := strings.TrimLeft(strings.Trim(field, `"'`), "-")
		if value, ok := strings.CutPrefix(flag, "tags="); ok {
			tags = strings.Split(value, ",")
		}
	}
	return tags
}

func unsupportedSourceGoFlags(flags string) bool {
	for _, field := range strings.Fields(flags) {
		flag := strings.TrimLeft(strings.Trim(field, `"'`), "-")
		if flag == "overlay" || strings.HasPrefix(flag, "overlay=") ||
			flag == "modfile" || strings.HasPrefix(flag, "modfile=") ||
			flag == "toolexec" || strings.HasPrefix(flag, "toolexec=") {
			return true
		}
	}
	return false
}

type sourceOutput struct {
	bytes.Buffer
	limit int
}

func (buffer *sourceOutput) Write(data []byte) (int, error) {
	if buffer.Len()+len(data) > buffer.limit {
		return 0, ErrSourceInventory
	}
	return buffer.Buffer.Write(data)
}

func sourceGoOutput(ctx context.Context, directory string, env []string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, goTimeout)
	defer cancel()
	command := exec.CommandContext(bounded, "go", args...)
	command.Dir = directory
	command.Env = env
	stdout := &sourceOutput{limit: maxGoOutput}
	stderr := &sourceOutput{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return nil, ErrSourceInventory
	}
	return stdout.Bytes(), nil
}
