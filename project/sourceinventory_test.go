package project_test

import (
	"archive/zip"
	"context"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/plystra/data/project"
)

func TestDiscoverDataRootsSelectsAuthoredGoFiles(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "model/member.go", "package model\n//plystra:data accounts.user/v1\nvar Member = 1\n")
	writeProjectFile(t, root, "model/helper.go", "package model\nvar Helper = 2\n")
	writeProjectFile(t, root, "model/platform_windows.go", "package model\nvar Platform = 3\n")
	writeProjectFile(t, root, "model/platform_linux.go", "package model\nvar Platform = 3\n")
	writeProjectFile(t, root, "model/tagged.go", "//go:build customdata\n\npackage model\nvar Tagged = 4\n")
	writeProjectFile(t, root, "generated/ignored.go", "package generated\n//plystra:data generated.member/v1\n")
	writeProjectFile(t, root, "dataaccess/ignored.go", "package dataaccess\n//plystra:data generated.access/v1\n")
	writeProjectFile(t, root, "fixtures/ignored.go", "package fixtures\n//plystra:data fixture.member/v1\n")
	writeProjectFile(t, root, "large/irrelevant.go", "package large\n// "+strings.Repeat("x", 5<<20)+"\n")
	writeProjectFile(t, root, "other/false.go", "package other\nconst text = `//plystra:data fake.member/v1`\n")
	writeProjectFile(t, root, "other/member_test.go", "package other\n//plystra:data test.member/v1\n")
	writeProjectFile(t, root, "excluded/member.go", "//go:build neverdata\n\npackage excluded\n//plystra:data excluded.member/v1\n")
	writeProjectFile(t, root, "excluded/helper.go", "package excluded\nvar Helper = 1\n")
	result, err := project.DiscoverDataRoots(context.Background(), root, project.SourceOptions{Offline: true})
	if err != nil || len(result.Packages) != 1 || result.Packages[0].ImportPath != "example.com/app/model" || result.Packages[0].Module.Origin != "current" {
		t.Fatalf("inventory = %#v, %v", result, err)
	}
	paths := make([]string, 0, len(result.Packages[0].Files))
	for _, file := range result.Packages[0].Files {
		paths = append(paths, file.Path)
	}
	joined := strings.Join(paths, ",")
	if !strings.Contains(joined, "model/helper.go") || !strings.Contains(joined, "model/member.go") ||
		!strings.Contains(joined, "model/platform_"+runtime.GOOS+".go") || len(paths) != 3 {
		t.Fatalf("selected files = %v", paths)
	}
	tagged, err := project.DiscoverDataRoots(context.Background(), root, project.SourceOptions{Offline: true, BuildTags: []string{"customdata"}})
	if err != nil || len(tagged.Packages) != 1 || len(tagged.Packages[0].Files) != 4 || len(tagged.Build.BuildTags) != 1 || tagged.Build.BuildTags[0] != "customdata" {
		t.Fatalf("tagged inventory = %#v, %v", tagged, err)
	}
}

func TestDiscoverDataRootsIncludesDependencyProject(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\nrequire example.com/reuse v0.1.0\nreplace example.com/reuse => ./reuse\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "reuse/go.mod", "module example.com/reuse\ngo 1.26\n")
	writeProjectFile(t, root, "reuse/plystra.yaml", "project: reuse\n")
	writeProjectFile(t, root, "reuse/persistence/member.go", "package persistence\n//plystra:data reuse.member/v1\nvar Member = 1\n")
	writeProjectFile(t, root, "reuse/internal/private/member.go", "package private\n//plystra:data reuse.private/v1\nvar Member = 1\n")
	result, err := project.DiscoverDataRoots(context.Background(), root, project.SourceOptions{Offline: true})
	if err != nil || len(result.Packages) != 1 || result.Packages[0].ImportPath != "example.com/reuse/persistence" {
		t.Fatalf("dependency inventory = %#v, %v", result, err)
	}
}

func TestDiscoverDataRootsUsesGoFlagsTagsAndBareDirective(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "model/member.go", "//go:build customdata\n\npackage model\n//plystra:data\nvar Member = 1\n")
	t.Setenv("GOFLAGS", "-tags=customdata")
	result, err := project.DiscoverDataRoots(context.Background(), root, project.SourceOptions{Offline: true})
	if err != nil || len(result.Packages) != 1 || len(result.Build.BuildTags) != 1 || result.Build.BuildTags[0] != "customdata" {
		t.Fatalf("environment tags inventory = %#v, %v", result, err)
	}
}

func TestDiscoverDataRootsRejectsAuthoredGeneratedImport(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "model/member.go", "package model\nimport _ \"example.com/app/dataaccess/member\"\n//plystra:data accounts.user/v1\nvar Member = 1\n")
	if _, err := project.DiscoverDataRoots(context.Background(), root, project.SourceOptions{Offline: true}); !errors.Is(err, project.ErrSourceInventory) || !strings.Contains(err.Error(), "dataaccess") {
		t.Fatalf("generated import error = %v", err)
	}
}

func TestDiscoverDataRootsRejectsGeneratedImportOutsideRootPackage(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "other/member.go", "package other\n//plystra:data accounts.user/v1\nvar Member = 1\n")
	writeProjectFile(t, root, "service/service.go", "package service\nimport _ \"example.com/app/dataaccess/member\"\n")
	if _, err := project.DiscoverDataRoots(context.Background(), root, project.SourceOptions{Offline: true}); !errors.Is(err, project.ErrSourceInventory) || !strings.Contains(err.Error(), "dataaccess") {
		t.Fatalf("generated import error = %v", err)
	}
}

func TestDiscoverDataRootsHonorsWorkspaceModule(t *testing.T) {
	root := t.TempDir()
	app := filepath.Join(root, "app")
	writeProjectFile(t, root, "go.work", "go 1.26\nuse (\n./app\n./reuse\n)\n")
	writeProjectFile(t, app, "go.mod", "module example.com/app\ngo 1.26\n")
	writeProjectFile(t, app, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "reuse/go.mod", "module example.com/reuse\ngo 1.26\n")
	writeProjectFile(t, root, "reuse/plystra.yaml", "project: reuse\n")
	writeProjectFile(t, root, "reuse/model/member.go", "package model\n//plystra:data reuse.member/v1\nvar Member = 1\n")
	t.Setenv("GOWORK", filepath.Join(root, "go.work"))
	result, err := project.DiscoverDataRoots(context.Background(), app, project.SourceOptions{Offline: true})
	if err != nil || len(result.Packages) != 1 || result.Packages[0].Module.Origin != "workspace" || result.Packages[0].Module.Path != "example.com/reuse" {
		t.Fatalf("workspace inventory = %#v, %v", result, err)
	}
}

func TestDiscoverDataRootsVerifiesVersionedReplacement(t *testing.T) {
	root := t.TempDir()
	proxy := filepath.Join(root, "proxy")
	app := filepath.Join(root, "app")
	writeProjectFile(t, app, "go.mod", "module example.com/app\ngo 1.26\nrequire example.com/reuse v0.1.0\nreplace example.com/reuse => example.com/fork v0.2.0\n")
	writeProjectFile(t, app, "plystra.yaml", "project: app\n")
	writeProxyProject(t, proxy, "example.com/fork", "v0.2.0", map[string]string{
		"go.mod":          "module example.com/fork\ngo 1.26\n",
		"plystra.yaml":    "project: fork\n",
		"model/member.go": "package model\n//plystra:data fork.member/v1\nvar Member = 1\n",
	})
	proxyURL := (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(proxy)}).String()
	t.Setenv("GOPROXY", proxyURL)
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", filepath.Join(root, "cache"))
	t.Setenv("GOWORK", "off")
	command := exec.Command("go", "mod", "download", "example.com/fork@v0.2.0")
	command.Dir = app
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("download replacement: %v: %s", err, output)
	}
	result, err := project.DiscoverDataRoots(context.Background(), app, project.SourceOptions{Offline: true})
	if err != nil || len(result.Packages) != 1 || result.Packages[0].ImportPath != "example.com/reuse/model" ||
		result.Packages[0].Module.Origin != "versioned-replacement" || result.Packages[0].Module.Replacement != "example.com/fork@v0.2.0" ||
		!strings.HasPrefix(result.Packages[0].Module.Checksum, "h1:") {
		t.Fatalf("versioned replacement = %#v, %v", result, err)
	}
	cachedFile := filepath.Join(root, "cache", "example.com", "fork@v0.2.0", "model", "member.go")
	original, err := os.ReadFile(cachedFile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(cachedFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachedFile, []byte("package model\nvar Member = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := project.DiscoverDataRoots(context.Background(), app, project.SourceOptions{Offline: true}); !errors.Is(err, project.ErrSourceInventory) {
		t.Fatalf("tampered cache error = %v", err)
	}
	if err := os.WriteFile(cachedFile, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(app, "go.sum")); err != nil {
		t.Fatal(err)
	}
	if _, err := project.DiscoverDataRoots(context.Background(), app, project.SourceOptions{Offline: true}); !errors.Is(err, project.ErrSourceInventory) {
		t.Fatalf("unpinned replacement error = %v", err)
	}
}

func writeProxyProject(t *testing.T, proxy, modulePath, version string, files map[string]string) {
	t.Helper()
	root := filepath.Join(proxy, filepath.FromSlash(modulePath), "@v")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, version+".mod"), []byte(files["go.mod"]), 0o600); err != nil {
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
	for name, content := range files {
		entry, err := writer.Create(modulePath + "@" + version + "/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
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

func writeProjectFile(t *testing.T, root, relative, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
