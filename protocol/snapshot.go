package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/plystra/data/analyzer"
)

var ErrInvalidSnapshotManifest = errors.New("invalid Data source snapshot manifest")

type SnapshotFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Bytes  int    `json:"bytes"`
}

type SnapshotPackage struct {
	ImportPath      string                   `json:"import_path"`
	RootEligibility analyzer.RootEligibility `json:"root_eligibility"`
	Files           []SnapshotFile           `json:"files"`
}

// SnapshotManifest names only a private source root and bounded file identities.
// Source bytes never travel in the analyze protocol frame.
type SnapshotManifest struct {
	Root      string                      `json:"root"`
	Packages  []SnapshotPackage           `json:"packages"`
	Resources []analyzer.ResourceContract `json:"resources"`
	Limits    analyzer.Limits             `json:"limits"`
}

// WriteSnapshot materializes a validated source inventory in a new private root.
// The caller owns the root's lifetime and must remove it after compiler use.
func WriteSnapshot(root string, source analyzer.Snapshot) (SnapshotManifest, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return SnapshotManifest{}, ErrInvalidSnapshotManifest
	}
	total, count := 0, 0
	for _, pkg := range source.Packages {
		for _, file := range pkg.Files {
			if count >= MaxSnapshotFiles || len(file.Content) > MaxSourceFileBytes ||
				len(file.Content) > MaxSnapshotBytes-total || len(file.Path) > MaxSourcePathBytes {
				return SnapshotManifest{}, ErrInvalidSnapshotManifest
			}
			count++
			total += len(file.Content)
		}
	}
	if _, err := analyzer.SnapshotDigest(source); err != nil {
		return SnapshotManifest{}, fmt.Errorf("%w: %v", ErrInvalidSnapshotManifest, err)
	}
	manifest := SnapshotManifest{Root: root, Resources: append([]analyzer.ResourceContract(nil), source.Resources...), Limits: source.Limits}
	packages := append([]analyzer.Package(nil), source.Packages...)
	sort.Slice(packages, func(i, j int) bool { return packages[i].ImportPath < packages[j].ImportPath })
	for _, pkg := range packages {
		entry := SnapshotPackage{ImportPath: pkg.ImportPath, RootEligibility: pkg.RootEligibility}
		files := append([]analyzer.SourceFile(nil), pkg.Files...)
		sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
		for _, file := range files {
			entry.Files = append(entry.Files, SnapshotFile{Path: file.Path, Digest: digestBytes(file.Content), Bytes: len(file.Content)})
		}
		manifest.Packages = append(manifest.Packages, entry)
	}
	if err := validateSnapshotManifest(manifest); err != nil {
		return SnapshotManifest{}, err
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		return SnapshotManifest{}, fmt.Errorf("%w: private root is unavailable", ErrInvalidSnapshotManifest)
	}
	complete := false
	defer func() {
		if !complete {
			_ = os.RemoveAll(root)
		}
	}()
	for index, pkg := range packages {
		for _, file := range pkg.Files {
			path := snapshotFilePath(root, index, file.Path)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return SnapshotManifest{}, fmt.Errorf("%w: cannot create private source directory", ErrInvalidSnapshotManifest)
			}
			if err := os.WriteFile(path, file.Content, 0o400); err != nil {
				return SnapshotManifest{}, fmt.Errorf("%w: cannot write private source file", ErrInvalidSnapshotManifest)
			}
		}
	}
	complete = true
	return manifest, nil
}

// SnapshotManifestDigest binds every declared source identity but not the
// temporary absolute root, so equivalent snapshots have stable digests.
func SnapshotManifestDigest(manifest SnapshotManifest) (string, error) {
	if err := validateSnapshotManifest(manifest); err != nil {
		return "", err
	}
	identity := manifest
	identity.Root = ""
	identity.Packages = append([]SnapshotPackage(nil), manifest.Packages...)
	for index := range identity.Packages {
		identity.Packages[index].Files = append([]SnapshotFile(nil), identity.Packages[index].Files...)
		sort.Slice(identity.Packages[index].Files, func(i, j int) bool {
			return identity.Packages[index].Files[i].Path < identity.Packages[index].Files[j].Path
		})
	}
	sort.Slice(identity.Packages, func(i, j int) bool { return identity.Packages[i].ImportPath < identity.Packages[j].ImportPath })
	identity.Resources = append([]analyzer.ResourceContract(nil), manifest.Resources...)
	sort.Slice(identity.Resources, func(i, j int) bool {
		if identity.Resources[i].ID != identity.Resources[j].ID {
			return identity.Resources[i].ID < identity.Resources[j].ID
		}
		if identity.Resources[i].ImportPath != identity.Resources[j].ImportPath {
			return identity.Resources[i].ImportPath < identity.Resources[j].ImportPath
		}
		return identity.Resources[i].TypeName < identity.Resources[j].TypeName
	})
	payload, err := json.Marshal(identity)
	if err != nil {
		return "", fmt.Errorf("%w: encode identity", ErrInvalidSnapshotManifest)
	}
	return digestBytes(append([]byte("plystra.data.snapshot-manifest/v1\x00"), payload...)), nil
}

// LoadSnapshot checks each private file before handing source bytes to the analyzer.
func LoadSnapshot(manifest SnapshotManifest) (analyzer.Snapshot, error) {
	if err := validateSnapshotManifest(manifest); err != nil {
		return analyzer.Snapshot{}, err
	}
	rootInfo, err := os.Lstat(manifest.Root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return analyzer.Snapshot{}, ErrInvalidSnapshotManifest
	}
	privateRoot, err := os.OpenRoot(manifest.Root)
	if err != nil {
		return analyzer.Snapshot{}, ErrInvalidSnapshotManifest
	}
	defer privateRoot.Close()
	ordered := append([]SnapshotPackage(nil), manifest.Packages...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ImportPath < ordered[j].ImportPath })
	snapshot := analyzer.Snapshot{Resources: append([]analyzer.ResourceContract(nil), manifest.Resources...), Limits: manifest.Limits}
	for index, pkg := range ordered {
		entry := analyzer.Package{ImportPath: pkg.ImportPath, RootEligibility: pkg.RootEligibility}
		for _, file := range pkg.Files {
			relative := snapshotRelativePath(index, file.Path)
			if err := checkSnapshotPath(privateRoot, relative); err != nil {
				return analyzer.Snapshot{}, err
			}
			opened, err := privateRoot.Open(relative)
			if err != nil {
				return analyzer.Snapshot{}, ErrInvalidSnapshotManifest
			}
			info, statErr := opened.Stat()
			if statErr != nil || !info.Mode().IsRegular() || info.Size() != int64(file.Bytes) {
				opened.Close()
				return analyzer.Snapshot{}, ErrInvalidSnapshotManifest
			}
			content, readErr := io.ReadAll(io.LimitReader(opened, MaxSourceFileBytes+1))
			closeErr := opened.Close()
			if readErr != nil || closeErr != nil || len(content) != file.Bytes || digestBytes(content) != file.Digest {
				return analyzer.Snapshot{}, ErrInvalidSnapshotManifest
			}
			entry.Files = append(entry.Files, analyzer.SourceFile{Path: file.Path, Content: content})
		}
		snapshot.Packages = append(snapshot.Packages, entry)
	}
	if _, err := analyzer.SnapshotDigest(snapshot); err != nil {
		return analyzer.Snapshot{}, fmt.Errorf("%w: %v", ErrInvalidSnapshotManifest, err)
	}
	return snapshot, nil
}

func validateSnapshotManifest(manifest SnapshotManifest) error {
	if !filepath.IsAbs(manifest.Root) || filepath.Clean(manifest.Root) != manifest.Root {
		return ErrInvalidSnapshotManifest
	}
	limits := manifest.Limits
	if limits.MaxRoots > MaxRoots || limits.MaxNodes > MaxNodes || limits.MaxImports > MaxImports ||
		limits.MaxNesting > MaxNesting || limits.MaxSymbolBytes > MaxSymbolBytes ||
		limits.MaxDiagnostics > MaxDiagnostics || limits.MaxDiagnosticBytes > MaxDiagnosticBytes {
		return ErrInvalidSnapshotManifest
	}
	validation := analyzer.Snapshot{Resources: manifest.Resources, Limits: manifest.Limits}
	seen := make(map[string]struct{})
	total, count := 0, 0
	for _, pkg := range manifest.Packages {
		entry := analyzer.Package{ImportPath: pkg.ImportPath, RootEligibility: pkg.RootEligibility}
		for _, file := range pkg.Files {
			if len(file.Path) > MaxSourcePathBytes || file.Bytes < 0 || file.Bytes > MaxSourceFileBytes || !validDigest(file.Digest) {
				return ErrInvalidSnapshotManifest
			}
			if _, exists := seen[pkg.ImportPath+"\x00"+file.Path]; exists {
				return ErrInvalidSnapshotManifest
			}
			seen[pkg.ImportPath+"\x00"+file.Path] = struct{}{}
			count++
			total += file.Bytes
			if count > MaxSnapshotFiles || total > MaxSnapshotBytes {
				return ErrInvalidSnapshotManifest
			}
			entry.Files = append(entry.Files, analyzer.SourceFile{Path: file.Path, Content: []byte("x")})
		}
		validation.Packages = append(validation.Packages, entry)
	}
	if _, err := analyzer.SnapshotDigest(validation); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidSnapshotManifest, err)
	}
	return nil
}

func snapshotFilePath(root string, packageIndex int, path string) string {
	return filepath.Join(root, snapshotRelativePath(packageIndex, path))
}

func snapshotRelativePath(packageIndex int, path string) string {
	return filepath.Join("packages", fmt.Sprintf("%04d", packageIndex), filepath.FromSlash(path))
}

func checkSnapshotPath(root *os.Root, relative string) error {
	current := ""
	for _, part := range strings.Split(filepath.ToSlash(relative), "/") {
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrInvalidSnapshotManifest
		}
		if current == relative && !info.Mode().IsRegular() {
			return ErrInvalidSnapshotManifest
		}
	}
	return nil
}
