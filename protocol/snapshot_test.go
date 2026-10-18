package protocol_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/protocol"
)

func TestPrivateSnapshotSeparatesPackagesAndStabilizesDigest(t *testing.T) {
	source := analyzer.Snapshot{Packages: []analyzer.Package{
		{ImportPath: "example.com/alpha", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package alpha\n")}}},
		{ImportPath: "example.com/beta", RootEligibility: analyzer.RootSupport, Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package beta\n")}}},
	}}
	first, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), source)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := protocol.SnapshotManifestDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := protocol.SnapshotManifestDigest(second)
	if err != nil || firstDigest != secondDigest {
		t.Fatalf("snapshot digests = %q, %q, %v", firstDigest, secondDigest, err)
	}
	loaded, err := protocol.LoadSnapshot(first)
	if err != nil || len(loaded.Packages) != 2 || string(loaded.Packages[0].Files[0].Content) != "package alpha\n" || string(loaded.Packages[1].Files[0].Content) != "package beta\n" {
		t.Fatalf("loaded snapshot = %#v, %v", loaded, err)
	}
}

func TestPrivateSnapshotStagesVerifiedStandardExports(t *testing.T) {
	source := analyzer.Snapshot{
		Packages:        []analyzer.Package{{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\n")}}}},
		StandardExports: []analyzer.StandardExport{{ImportPath: "fmt", Content: []byte("selected-export")}},
	}
	first, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), source)
	if err != nil {
		t.Fatal(err)
	}
	second, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), source)
	if err != nil {
		t.Fatal(err)
	}
	firstDigest, err := protocol.SnapshotManifestDigest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := protocol.SnapshotManifestDigest(second)
	if err != nil || firstDigest != secondDigest {
		t.Fatalf("standard snapshot digests = %q, %q, %v", firstDigest, secondDigest, err)
	}
	changed := first
	changed.StandardExports = append([]protocol.SnapshotStandardExport(nil), first.StandardExports...)
	changed.StandardExports[0].Digest = "sha256:" + strings.Repeat("0", 64)
	changedDigest, err := protocol.SnapshotManifestDigest(changed)
	if err != nil || changedDigest == firstDigest {
		t.Fatalf("changed standard digest = %q, %v", changedDigest, err)
	}
	loaded, err := protocol.LoadSnapshot(first)
	if err != nil || len(loaded.StandardExports) != 1 || string(loaded.StandardExports[0].Content) != "selected-export" {
		t.Fatalf("loaded standard export = %#v, %v", loaded.StandardExports, err)
	}
	path := filepath.Join(first.Root, "standard", "0000.a")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed-export"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.LoadSnapshot(first); !errors.Is(err, protocol.ErrInvalidSnapshotManifest) {
		t.Fatalf("changed standard export error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "export.a")
	if err := os.WriteFile(external, []byte("selected-export"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	if _, err := protocol.LoadSnapshot(first); !errors.Is(err, protocol.ErrInvalidSnapshotManifest) {
		t.Fatalf("symlinked standard export error = %v", err)
	}
}

func TestPrivateSnapshotRejectsChangedSourceAndSymlink(t *testing.T) {
	source := analyzer.Snapshot{Packages: []analyzer.Package{{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\n")}}}}}
	manifest, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(manifest.Root, "packages", "0000", "model.go")
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := protocol.LoadSnapshot(manifest); !errors.Is(err, protocol.ErrInvalidSnapshotManifest) {
		t.Fatalf("changed source error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external.go")
	if err := os.WriteFile(external, []byte("package model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, path); err != nil {
		t.Skipf("file symlinks unavailable: %v", err)
	}
	if _, err := protocol.LoadSnapshot(manifest); !errors.Is(err, protocol.ErrInvalidSnapshotManifest) {
		t.Fatalf("symlink error = %v", err)
	}
}

func TestSnapshotManifestRejectsInvalidPathsAndBounds(t *testing.T) {
	source := analyzer.Snapshot{
		Packages:        []analyzer.Package{{ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible, Files: []analyzer.SourceFile{{Path: "model.go", Content: []byte("package model\n")}}}},
		StandardExports: []analyzer.StandardExport{{ImportPath: "fmt", Content: []byte("export")}},
	}
	base, err := protocol.WriteSnapshot(filepath.Join(t.TempDir(), "source"), source)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		change func(*protocol.SnapshotManifest)
	}{
		{"traversal", func(m *protocol.SnapshotManifest) { m.Packages[0].Files[0].Path = "../outside.go" }},
		{"drive path", func(m *protocol.SnapshotManifest) { m.Packages[0].Files[0].Path = "C:/outside.go" }},
		{"device name", func(m *protocol.SnapshotManifest) { m.Packages[0].Files[0].Path = "NUL.go" }},
		{"alternate stream", func(m *protocol.SnapshotManifest) { m.Packages[0].Files[0].Path = "model.go:stream" }},
		{"oversized file", func(m *protocol.SnapshotManifest) { m.Packages[0].Files[0].Bytes = protocol.MaxSourceFileBytes + 1 }},
		{"invalid digest", func(m *protocol.SnapshotManifest) { m.Packages[0].Files[0].Digest = "sha256:bad" }},
		{"oversized analysis limit", func(m *protocol.SnapshotManifest) { m.Limits.MaxRoots = protocol.MaxRoots + 1 }},
		{"standard traversal", func(m *protocol.SnapshotManifest) { m.StandardExports[0].ImportPath = "../fmt" }},
		{"standard source collision", func(m *protocol.SnapshotManifest) { m.StandardExports[0].ImportPath = "example.com/model" }},
		{"standard zero size", func(m *protocol.SnapshotManifest) { m.StandardExports[0].Bytes = 0 }},
		{"standard oversized file", func(m *protocol.SnapshotManifest) { m.StandardExports[0].Bytes = protocol.MaxStandardFileBytes + 1 }},
		{"standard invalid digest", func(m *protocol.SnapshotManifest) { m.StandardExports[0].Digest = "sha256:bad" }},
		{"standard duplicate", func(m *protocol.SnapshotManifest) {
			m.StandardExports = append(m.StandardExports, m.StandardExports[0])
		}},
		{"too many standard exports", func(m *protocol.SnapshotManifest) {
			m.StandardExports = nil
			for index := 0; index <= protocol.MaxStandardExports; index++ {
				m.StandardExports = append(m.StandardExports, protocol.SnapshotStandardExport{ImportPath: fmt.Sprintf("std%d", index), Digest: base.StandardExports[0].Digest, Bytes: 1})
			}
		}},
		{"too many standard bytes", func(m *protocol.SnapshotManifest) {
			m.StandardExports = nil
			for index := 0; index < 9; index++ {
				m.StandardExports = append(m.StandardExports, protocol.SnapshotStandardExport{ImportPath: fmt.Sprintf("std%d", index), Digest: base.StandardExports[0].Digest, Bytes: protocol.MaxStandardFileBytes})
			}
		}},
		{"too many files", func(m *protocol.SnapshotManifest) {
			m.Packages[0].Files = nil
			for index := 0; index <= protocol.MaxSnapshotFiles; index++ {
				m.Packages[0].Files = append(m.Packages[0].Files, protocol.SnapshotFile{Path: fmt.Sprintf("%d.go", index), Digest: base.Packages[0].Files[0].Digest, Bytes: 1})
			}
		}},
		{"too many bytes", func(m *protocol.SnapshotManifest) {
			m.Packages[0].Files = nil
			for index := 0; index < 17; index++ {
				m.Packages[0].Files = append(m.Packages[0].Files, protocol.SnapshotFile{Path: fmt.Sprintf("%d.go", index), Digest: base.Packages[0].Files[0].Digest, Bytes: protocol.MaxSourceFileBytes})
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			manifest := base
			manifest.Packages = append([]protocol.SnapshotPackage(nil), base.Packages...)
			manifest.Packages[0].Files = append([]protocol.SnapshotFile(nil), base.Packages[0].Files...)
			manifest.StandardExports = append([]protocol.SnapshotStandardExport(nil), base.StandardExports...)
			test.change(&manifest)
			if _, err := protocol.SnapshotManifestDigest(manifest); !errors.Is(err, protocol.ErrInvalidSnapshotManifest) {
				t.Fatalf("invalid manifest digest error = %v", err)
			}
		})
	}
	invalidSource := source
	invalidSource.Packages = append([]analyzer.Package(nil), source.Packages...)
	invalidSource.Packages[0].Files = []analyzer.SourceFile{{Path: "../outside.go", Content: []byte("package model\n")}}
	root := filepath.Join(t.TempDir(), "source")
	if _, err := protocol.WriteSnapshot(root, invalidSource); !errors.Is(err, protocol.ErrInvalidSnapshotManifest) {
		t.Fatalf("invalid source error = %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("invalid source created a private root: %v", err)
	}
}

func TestSnapshotFrameDoesNotEmbedSource(t *testing.T) {
	request := validRequest(t)
	var frame strings.Builder
	if err := protocol.WriteFrame(&frame, request); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(frame.String(), "var Accounts = declaration.Member") {
		t.Fatal("analyze frame embedded authored source bytes")
	}
}

func TestWriteSnapshotRemovesPartialPrivateRoot(t *testing.T) {
	source := analyzer.Snapshot{Packages: []analyzer.Package{{
		ImportPath: "example.com/model", RootEligibility: analyzer.RootEligible,
		Files: []analyzer.SourceFile{
			{Path: "a.go", Content: []byte("package model\n")},
			{Path: "a.go/b.go", Content: []byte("package model\n")},
		},
	}}}
	root := filepath.Join(t.TempDir(), "source")
	if _, err := protocol.WriteSnapshot(root, source); !errors.Is(err, protocol.ErrInvalidSnapshotManifest) {
		t.Fatalf("conflicting private paths error = %v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial private root remained: %v", err)
	}
}
