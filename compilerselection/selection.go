package compilerselection

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/plystra/data/protocol"
	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
)

const (
	manifestName   = "plystra-data-compiler.json"
	maxProjectFile = 8 << 20
	maxGoOutput    = 8 << 20
	goTimeout      = 45 * time.Second
)

var (
	ErrUnavailable      = errors.New("selected Data compiler is unavailable")
	ErrInvalidSelection = errors.New("selected Data compiler is invalid")
	ErrConcurrentChange = errors.New("Project module inputs changed during Data compiler selection")
	errOutputLimit      = errors.New("Go command output exceeds limit")
)

type Options struct {
	Offline bool
}

// Selection binds a Go-selected module source to its exact compiler manifest.
type Selection struct {
	ModuleVersion  string
	ModuleChecksum string
	ManifestDigest string
	Manifest       protocol.DistributionManifest
	GoToolchain    string
	Root           string
}

// Resolve verifies the selected Data module without changing Project files.
func Resolve(ctx context.Context, projectRoot string, options Options) (Selection, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return Selection{}, ErrUnavailable
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return Selection{}, ErrUnavailable
	}
	beforeMod, err := snapshotFile(filepath.Join(root, "go.mod"), true)
	if err != nil {
		return Selection{}, ErrUnavailable
	}
	beforeSum, err := snapshotFile(filepath.Join(root, "go.sum"), false)
	if err != nil {
		return Selection{}, ErrUnavailable
	}
	goMod, err := readRegular(filepath.Join(root, "go.mod"), maxProjectFile)
	if err != nil {
		return Selection{}, ErrUnavailable
	}
	parsed, err := modfile.Parse("go.mod", goMod, nil)
	if err != nil {
		return Selection{}, ErrUnavailable
	}
	for _, replacement := range parsed.Replace {
		if replacement.Old.Path == protocol.ModulePath {
			return Selection{}, ErrInvalidSelection
		}
	}
	environment := goEnvironment(options.Offline)
	listedBytes, err := goOutput(ctx, root, environment, "list", "-m", "-json", "-mod=readonly", "all")
	if err != nil {
		return Selection{}, fmt.Errorf("%w: resolve the Project module graph", ErrUnavailable)
	}
	listed, err := selectedModule(listedBytes)
	if err != nil {
		return Selection{}, err
	}
	if listed.Main || listed.Replace != nil || module.Check(protocol.ModulePath, listed.Version) != nil {
		return Selection{}, ErrInvalidSelection
	}
	expectedSum, err := projectChecksum(root, listed.Version, beforeSum)
	if err != nil {
		return Selection{}, err
	}
	if listed.Sum != "" && listed.Sum != expectedSum {
		return Selection{}, ErrInvalidSelection
	}
	isolatedRoot, err := os.MkdirTemp("", "plystra-data-download-")
	if err != nil {
		return Selection{}, ErrUnavailable
	}
	defer os.RemoveAll(isolatedRoot)
	downloadedBytes, err := goOutput(ctx, isolatedRoot, environment, "mod", "download", "-json", protocol.ModulePath+"@"+listed.Version)
	if err != nil {
		return Selection{}, fmt.Errorf("%w: download the selected module", ErrUnavailable)
	}
	var downloaded downloadedModule
	if err := decodeOne(downloadedBytes, &downloaded); err != nil || downloaded.Path != protocol.ModulePath || downloaded.Version != listed.Version || !validChecksum(downloaded.Sum) {
		return Selection{}, ErrInvalidSelection
	}
	if downloaded.Sum != expectedSum {
		return Selection{}, ErrInvalidSelection
	}
	if listed.Dir != "" && !samePath(listed.Dir, downloaded.Dir) {
		return Selection{}, ErrInvalidSelection
	}
	moduleRoot, err := verifySource(downloaded.Dir, listed.Version, downloaded.Sum)
	if err != nil {
		return Selection{}, err
	}
	manifest, digest, err := readManifest(moduleRoot)
	if err != nil {
		return Selection{}, err
	}
	if _, err := verifySource(moduleRoot, listed.Version, downloaded.Sum); err != nil {
		return Selection{}, err
	}
	toolchain, err := goOutput(ctx, isolatedRoot, environment, "version")
	if err != nil {
		return Selection{}, ErrUnavailable
	}
	parts := strings.Fields(string(toolchain))
	if len(parts) < 3 || parts[0] != "go" || parts[1] != "version" || !strings.HasPrefix(parts[2], "go1.") {
		return Selection{}, ErrInvalidSelection
	}
	afterMod, modErr := snapshotFile(filepath.Join(root, "go.mod"), true)
	afterSum, sumErr := snapshotFile(filepath.Join(root, "go.sum"), false)
	if modErr != nil || sumErr != nil || beforeMod != afterMod || beforeSum != afterSum {
		return Selection{}, ErrConcurrentChange
	}
	return Selection{
		ModuleVersion: listed.Version, ModuleChecksum: downloaded.Sum,
		ManifestDigest: digest, Manifest: manifest, GoToolchain: parts[2], Root: moduleRoot,
	}, nil
}

type listedModule struct {
	Path    string          `json:"Path"`
	Version string          `json:"Version"`
	Sum     string          `json:"Sum"`
	Dir     string          `json:"Dir"`
	Main    bool            `json:"Main"`
	Replace json.RawMessage `json:"Replace"`
	Error   json.RawMessage `json:"Error"`
}

type downloadedModule struct {
	Path    string `json:"Path"`
	Version string `json:"Version"`
	Sum     string `json:"Sum"`
	Dir     string `json:"Dir"`
	Error   string `json:"Error"`
}

func selectedModule(data []byte) (listedModule, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var selected listedModule
	for {
		var item listedModule
		err := decoder.Decode(&item)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return listedModule{}, ErrInvalidSelection
		}
		if item.Path == protocol.ModulePath {
			if selected.Path != "" || len(item.Error) != 0 {
				return listedModule{}, ErrInvalidSelection
			}
			selected = item
		}
	}
	if selected.Path == "" {
		return listedModule{}, ErrUnavailable
	}
	return selected, nil
}

func decodeOne(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(value); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ErrInvalidSelection
	}
	return nil
}

func verifySource(root, version, checksum string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", ErrInvalidSelection
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(root))
	if err != nil {
		return "", ErrInvalidSelection
	}
	info, err := os.Lstat(canonical)
	if err != nil || !info.IsDir() {
		return "", ErrInvalidSelection
	}
	if err := filepath.WalkDir(canonical, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return ErrInvalidSelection
		}
		return nil
	}); err != nil {
		return "", ErrInvalidSelection
	}
	goMod, err := readRegular(filepath.Join(canonical, "go.mod"), maxProjectFile)
	if err != nil {
		return "", ErrInvalidSelection
	}
	parsed, err := modfile.Parse("go.mod", goMod, nil)
	if err != nil || parsed.Module == nil || parsed.Module.Mod.Path != protocol.ModulePath {
		return "", ErrInvalidSelection
	}
	actual, err := dirhash.HashDir(canonical, protocol.ModulePath+"@"+version, dirhash.Hash1)
	if err != nil || actual != checksum {
		return "", ErrInvalidSelection
	}
	return canonical, nil
}

func readManifest(root string) (protocol.DistributionManifest, string, error) {
	data, err := readRegular(filepath.Join(root, manifestName), protocol.MaxFrameBytes)
	if err != nil {
		return protocol.DistributionManifest{}, "", ErrInvalidSelection
	}
	manifest, digest, err := protocol.ParseDistributionManifest(data)
	if err != nil {
		return protocol.DistributionManifest{}, "", ErrInvalidSelection
	}
	return manifest, digest, nil
}

func readRegular(path string, maximum int) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > int64(maximum) {
		return nil, ErrInvalidSelection
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > maximum {
		return nil, ErrInvalidSelection
	}
	return data, nil
}

type fileSnapshot struct {
	present bool
	sum     [sha256.Size]byte
}

func snapshotFile(path string, required bool) (fileSnapshot, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) && !required {
		return fileSnapshot{}, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxProjectFile || (required && info.Size() == 0) {
		return fileSnapshot{}, ErrUnavailable
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) > maxProjectFile {
		return fileSnapshot{}, ErrUnavailable
	}
	return fileSnapshot{present: true, sum: sha256.Sum256(data)}, nil
}

func projectChecksum(root, version string, snapshot fileSnapshot) (string, error) {
	if !snapshot.present {
		return "", ErrUnavailable
	}
	data, err := os.ReadFile(filepath.Join(root, "go.sum"))
	if err != nil || len(data) > maxProjectFile {
		return "", ErrUnavailable
	}
	if sha256.Sum256(data) != snapshot.sum {
		return "", ErrConcurrentChange
	}
	var checksum string
	for _, line := range bytes.Split(data, []byte{'\n'}) {
		fields := bytes.Fields(line)
		if len(fields) != 3 || string(fields[0]) != protocol.ModulePath || string(fields[1]) != version {
			continue
		}
		value := string(fields[2])
		if !validChecksum(value) || (checksum != "" && checksum != value) {
			return "", ErrInvalidSelection
		}
		checksum = value
	}
	if checksum == "" {
		return "", ErrUnavailable
	}
	return checksum, nil
}

func validChecksum(value string) bool {
	if !strings.HasPrefix(value, "h1:") {
		return false
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(value, "h1:"))
	return err == nil && len(decoded) == sha256.Size
}

func samePath(a, b string) bool {
	if a == "" || b == "" {
		return false
	}
	left, leftErr := filepath.EvalSymlinks(a)
	right, rightErr := filepath.EvalSymlinks(b)
	return leftErr == nil && rightErr == nil && filepath.Clean(left) == filepath.Clean(right)
}

type limitedBuffer struct {
	bytes.Buffer
	limit int
}

func (buffer *limitedBuffer) Write(data []byte) (int, error) {
	if buffer.Len()+len(data) > buffer.limit {
		return 0, errOutputLimit
	}
	return buffer.Buffer.Write(data)
}

func goOutput(ctx context.Context, directory string, environment []string, args ...string) ([]byte, error) {
	bounded, cancel := context.WithTimeout(ctx, goTimeout)
	defer cancel()
	command := exec.CommandContext(bounded, "go", args...)
	command.Dir = directory
	command.Env = environment
	stdout := &limitedBuffer{limit: maxGoOutput}
	stderr := &limitedBuffer{limit: 4096}
	command.Stdout, command.Stderr = stdout, stderr
	if err := command.Run(); err != nil {
		return nil, ErrUnavailable
	}
	return stdout.Bytes(), nil
}

func goEnvironment(offline bool) []string {
	overrides := map[string]string{"GOWORK": "off", "GOFLAGS": "", "GOTOOLCHAIN": "local"}
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
