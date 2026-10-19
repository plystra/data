package compilerselection_test

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/plystra/data/compilerselection"
	"github.com/plystra/data/protocol"
)

func TestResolveSelectsGraphVersionAndPreservesProject(t *testing.T) {
	binary := buildPublicCommand(t)
	proxy, projectRoot := testProject(t, "module example.com/app\ngo 1.26\nrequire (\n github.com/plystra/data v0.1.0\n example.com/bridge v0.1.0\n)\n")
	manifest := publishedManifest(t)
	writeProxyModule(t, proxy, protocol.ModulePath, "v0.1.0", map[string][]byte{"go.mod": []byte("module github.com/plystra/data\ngo 1.26\n"), "plystra-data-compiler.json": manifest})
	writeProxyModule(t, proxy, protocol.ModulePath, "v0.2.0", map[string][]byte{"go.mod": []byte("module github.com/plystra/data\ngo 1.26\n"), "plystra-data-compiler.json": append(append([]byte(nil), manifest...), '\n')})
	writeProxyModule(t, proxy, "example.com/bridge", "v0.1.0", map[string][]byte{"go.mod": []byte("module example.com/bridge\ngo 1.26\nrequire github.com/plystra/data v0.2.0\n")})
	configureProxy(t, proxy, filepath.Join(t.TempDir(), "cache"))
	goInProject(t, projectRoot, "mod", "download")
	goInProject(t, projectRoot, "mod", "download", protocol.ModulePath+"@v0.2.0")
	beforeMod, _ := os.ReadFile(filepath.Join(projectRoot, "go.mod"))
	beforeSum, _ := os.ReadFile(filepath.Join(projectRoot, "go.sum"))
	selection, err := compilerselection.Resolve(context.Background(), projectRoot, compilerselection.Options{})
	if err != nil || selection.ModuleVersion != "v0.2.0" || selection.ManifestDigest == "" || selection.ModuleChecksum == "" || selection.Manifest.CommandImportPath != protocol.CommandImportPath {
		t.Fatalf("selected compiler = %#v, %v", selection, err)
	}
	secondManifest := append(append([]byte(nil), manifest...), '\n')
	sum := sha256.Sum256(secondManifest)
	if selection.ManifestDigest != "sha256:"+hex.EncodeToString(sum[:]) {
		t.Fatalf("raw manifest digest = %s", selection.ManifestDigest)
	}
	if _, err := compilerselection.Resolve(context.Background(), projectRoot, compilerselection.Options{Offline: true}); err != nil {
		t.Fatalf("warm offline selection = %v", err)
	}
	t.Setenv("GOTOOLCHAIN", "go1.99.0")
	if _, err := compilerselection.Resolve(context.Background(), projectRoot, compilerselection.Options{Offline: true}); !errors.Is(err, compilerselection.ErrUnavailable) {
		t.Fatalf("unavailable selected toolchain error = %v", err)
	}
	t.Setenv("GOTOOLCHAIN", "local")
	afterMod, _ := os.ReadFile(filepath.Join(projectRoot, "go.mod"))
	afterSum, _ := os.ReadFile(filepath.Join(projectRoot, "go.sum"))
	if !bytes.Equal(beforeMod, afterMod) || !bytes.Equal(beforeSum, afterSum) {
		t.Fatal("compiler inspection changed Project module files")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, binary, "inspect", "compiler", "--project", projectRoot, "--format", "json", "--offline")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("public inspection = %v, stderr = %q", err, stderr.String())
	}
	var output struct {
		Schema         string `json:"schema"`
		ModuleVersion  string `json:"module_version"`
		ManifestDigest string `json:"manifest_digest"`
		ModuleChecksum string `json:"module_checksum"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil || output.Schema != "plystra.data-compiler-inspection/v1" || output.ModuleVersion != "v0.2.0" || output.ManifestDigest != selection.ManifestDigest || output.ModuleChecksum != selection.ModuleChecksum {
		t.Fatalf("public compiler output = %#v, %v", output, err)
	}
}

func TestResolveRejectsReplacementAndMissingModule(t *testing.T) {
	proxy, root := testProject(t, "module example.com/app\ngo 1.26\nrequire github.com/plystra/data v0.1.0\n")
	manifest := publishedManifest(t)
	writeProxyModule(t, proxy, protocol.ModulePath, "v0.1.0", map[string][]byte{"go.mod": []byte("module github.com/plystra/data\ngo 1.26\n"), "plystra-data-compiler.json": manifest})
	writeProxyModule(t, proxy, protocol.ModulePath, "v0.2.0", map[string][]byte{"go.mod": []byte("module github.com/plystra/data\ngo 1.26\n"), "plystra-data-compiler.json": manifest})
	configureProxy(t, proxy, filepath.Join(t.TempDir(), "cache"))
	goInProject(t, root, "mod", "download", protocol.ModulePath+"@v0.1.0")
	for _, test := range []struct{ name, clause string }{
		{"local", "replace github.com/plystra/data => ./replacement\n"},
		{"versioned", "replace github.com/plystra/data v0.1.0 => github.com/plystra/data v0.2.0\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "local" {
				path := filepath.Join(root, "replacement")
				if err := os.MkdirAll(path, 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(path, "go.mod"), []byte("module github.com/plystra/data\ngo 1.26\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\ngo 1.26\nrequire github.com/plystra/data v0.1.0\n"+test.clause), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := compilerselection.Resolve(context.Background(), root, compilerselection.Options{}); !errors.Is(err, compilerselection.ErrInvalidSelection) {
				t.Fatalf("replacement error = %v", err)
			}
		})
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\ngo 1.26\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := compilerselection.Resolve(context.Background(), root, compilerselection.Options{}); !errors.Is(err, compilerselection.ErrUnavailable) {
		t.Fatalf("missing module error = %v", err)
	}
}

func TestResolveRejectsTamperedSourceAndMalformedManifest(t *testing.T) {
	proxy, root := testProject(t, "module example.com/app\ngo 1.26\nrequire github.com/plystra/data v0.1.0\n")
	manifest := publishedManifest(t)
	writeProxyModule(t, proxy, protocol.ModulePath, "v0.1.0", map[string][]byte{"go.mod": []byte("module github.com/plystra/data\ngo 1.26\n"), "plystra-data-compiler.json": manifest})
	configureProxy(t, proxy, filepath.Join(t.TempDir(), "cache"))
	goInProject(t, root, "mod", "download", protocol.ModulePath+"@v0.1.0")
	selection, err := compilerselection.Resolve(context.Background(), root, compilerselection.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(selection.Root, "plystra-data-compiler.json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(selection.Root, "plystra-data-compiler.json"), append(manifest, ' '), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := compilerselection.Resolve(context.Background(), root, compilerselection.Options{Offline: true}); !errors.Is(err, compilerselection.ErrInvalidSelection) {
		t.Fatalf("tampered source error = %v", err)
	}
	otherProxy, otherRoot := testProject(t, "module example.com/other\ngo 1.26\nrequire github.com/plystra/data v0.3.0\n")
	writeProxyModule(t, otherProxy, protocol.ModulePath, "v0.3.0", map[string][]byte{"go.mod": []byte("module github.com/plystra/data\ngo 1.26\n"), "plystra-data-compiler.json": []byte("{bad")})
	configureProxy(t, otherProxy, filepath.Join(t.TempDir(), "other-cache"))
	goInProject(t, otherRoot, "mod", "download", protocol.ModulePath+"@v0.3.0")
	if _, err := compilerselection.Resolve(context.Background(), otherRoot, compilerselection.Options{}); !errors.Is(err, compilerselection.ErrInvalidSelection) {
		t.Fatalf("invalid manifest error = %v", err)
	}
}

func TestResolveOfflineColdCacheFails(t *testing.T) {
	proxy, root := testProject(t, "module example.com/app\ngo 1.26\nrequire github.com/plystra/data v0.1.0\n")
	writeProxyModule(t, proxy, protocol.ModulePath, "v0.1.0", map[string][]byte{"go.mod": []byte("module github.com/plystra/data\ngo 1.26\n"), "plystra-data-compiler.json": publishedManifest(t)})
	configureProxy(t, proxy, filepath.Join(t.TempDir(), "warm-cache"))
	goInProject(t, root, "mod", "download", protocol.ModulePath+"@v0.1.0")
	t.Setenv("GOMODCACHE", filepath.Join(t.TempDir(), "cold-cache"))
	if _, err := compilerselection.Resolve(context.Background(), root, compilerselection.Options{Offline: true}); !errors.Is(err, compilerselection.ErrUnavailable) {
		t.Fatalf("cold offline error = %v", err)
	}
}

func TestResolveRejectsUnpinnedWarmCache(t *testing.T) {
	proxy, pinnedRoot := testProject(t, "module example.com/pinned\ngo 1.26\nrequire github.com/plystra/data v0.1.0\n")
	writeProxyModule(t, proxy, protocol.ModulePath, "v0.1.0", map[string][]byte{"go.mod": []byte("module github.com/plystra/data\ngo 1.26\n"), "plystra-data-compiler.json": publishedManifest(t)})
	configureProxy(t, proxy, filepath.Join(t.TempDir(), "cache"))
	goInProject(t, pinnedRoot, "mod", "download", protocol.ModulePath+"@v0.1.0")
	_, unpinnedRoot := testProject(t, "module example.com/unpinned\ngo 1.26\nrequire github.com/plystra/data v0.1.0\n")
	if _, err := compilerselection.Resolve(context.Background(), unpinnedRoot, compilerselection.Options{Offline: true}); !errors.Is(err, compilerselection.ErrUnavailable) {
		t.Fatalf("unpinned warm-cache error = %v", err)
	}
}

func testProject(t *testing.T, goMod string) (string, string) {
	t.Helper()
	base := t.TempDir()
	proxy, root := filepath.Join(base, "proxy"), filepath.Join(base, "project")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(goMod), 0o600); err != nil {
		t.Fatal(err)
	}
	return proxy, root
}

func configureProxy(t *testing.T, proxy, cache string) {
	t.Helper()
	proxyURL := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(proxy)}).String()
	t.Setenv("GOPROXY", proxyURL)
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", cache)
	t.Setenv("GOWORK", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOFLAGS", "")
}

func goInProject(t *testing.T, root string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go %s: %v: %s", strings.Join(args, " "), err, output)
	}
}

func publishedManifest(t *testing.T) []byte {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "plystra-data-compiler.json"))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func writeProxyModule(t *testing.T, proxy, path, version string, files map[string][]byte) {
	t.Helper()
	root := filepath.Join(proxy, filepath.FromSlash(path), "@v")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, version+".mod"), files["go.mod"], 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, version+".info"), []byte(`{"Version":"`+version+`","Time":"2026-01-01T00:00:00Z"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	archive, err := os.Create(filepath.Join(root, version+".zip"))
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(archive)
	for _, name := range []string{"go.mod", "plystra-data-compiler.json"} {
		content, exists := files[name]
		if !exists {
			continue
		}
		entry, err := writer.Create(path + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(content); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
}

func buildPublicCommand(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "plystra-data.exe")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "build", "-o", path, "../cmd/plystra-data")
	command.Env = append(os.Environ(), "GOWORK=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build public command: %v: %s", err, output)
	}
	return path
}
