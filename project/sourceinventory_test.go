package project_test

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/plystra/data/project"
	"github.com/plystra/data/protocol"
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

func TestDiscoverDataRootsRejectsGoSourceRedirection(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "model/member.go", "package model\n//plystra:data accounts.user/v1\nvar Member = 1\n")
	replacement := filepath.Join(t.TempDir(), "replacement.go")
	if err := os.WriteFile(replacement, []byte("package model\n//plystra:data accounts.user/v1\nvar Member = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	overlay, err := json.Marshal(struct {
		Replace map[string]string `json:"Replace"`
	}{Replace: map[string]string{filepath.Join(root, "model", "member.go"): replacement}})
	if err != nil {
		t.Fatal(err)
	}
	overlayPath := filepath.Join(t.TempDir(), "overlay.json")
	if err := os.WriteFile(overlayPath, overlay, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, flag := range []string{"-overlay=" + overlayPath, "--overlay=" + overlayPath, "-modfile=alternate.mod", "--modfile=alternate.mod", "-toolexec=wrapper", "--toolexec=wrapper"} {
		t.Run(flag[:strings.IndexByte(flag, '=')], func(t *testing.T) {
			t.Setenv("GOFLAGS", flag)
			if _, err := project.DiscoverDataRoots(context.Background(), root, project.SourceOptions{Offline: true}); !errors.Is(err, project.ErrSourceInventory) {
				t.Fatalf("redirected Go input error = %v", err)
			}
		})
	}
	goenv := filepath.Join(t.TempDir(), "goenv")
	if err := os.WriteFile(goenv, []byte("GOFLAGS=-overlay="+overlayPath+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOENV", goenv)
	previous, hadPrevious := os.LookupEnv("GOFLAGS")
	if err := os.Unsetenv("GOFLAGS"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if hadPrevious {
			_ = os.Setenv("GOFLAGS", previous)
		} else {
			_ = os.Unsetenv("GOFLAGS")
		}
	})
	if _, err := project.DiscoverDataRoots(context.Background(), root, project.SourceOptions{Offline: true}); !errors.Is(err, project.ErrSourceInventory) {
		t.Fatalf("persistent redirected Go input error = %v", err)
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

func TestDiscoverSourceClosureMarksSelectedImportsAsSupport(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\nrequire example.com/reuse v0.1.0\nreplace example.com/reuse => ./reuse\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "model/member.go", "package model\nimport (\"example.com/app/helper\"; \"example.com/reuse/support\")\n//plystra:data example.member/v1\nvar Member = helper.Value() + support.Value\n")
	writeProjectFile(t, root, "helper/helper.go", "package helper\nimport \"example.com/app/shared\"\nfunc Value() int { return shared.Value }\n")
	writeProjectFile(t, root, "shared/shared.go", "package shared\nimport \"fmt\"\nvar Value = len(fmt.Sprint(1))\n")
	writeProjectFile(t, root, "reuse/go.mod", "module example.com/reuse\ngo 1.26\n")
	writeProjectFile(t, root, "reuse/support/support.go", "package support\n//plystra:data not.a.root/v1\nconst Value = 2\n")
	result, err := project.DiscoverSourceClosure(context.Background(), root, project.SourceOptions{Offline: true})
	if err != nil || len(result.Packages) != 4 {
		t.Fatalf("support closure = %#v, %v", result, err)
	}
	want := map[string]string{
		"example.com/app/helper":    "support",
		"example.com/app/model":     "eligible",
		"example.com/app/shared":    "support",
		"example.com/reuse/support": "support",
	}
	for _, pkg := range result.Packages {
		if string(pkg.RootEligibility) != want[pkg.ImportPath] || len(pkg.Files) != 1 {
			t.Fatalf("package = %#v", pkg)
		}
		delete(want, pkg.ImportPath)
	}
	if len(want) != 0 {
		t.Fatalf("missing packages = %v", want)
	}
}

func TestStageAnalyzeInputAcquiresSelectedStandardExports(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "model/member.go", "package model\nimport \"fmt\"\n//plystra:data example.member/v1\nvar Member = fmt.Sprint(missingAuthoredName)\n")
	writeProjectFile(t, root, "model/tagged.go", "//go:build customdata\n\npackage model\nimport _ \"encoding/json\"\n")
	staged, err := project.StageAnalyzeInput(context.Background(), root, filepath.Join(t.TempDir(), "snapshot"), project.SourceOptions{Offline: true})
	if err != nil || staged.GoToolchain == "" || staged.Build.GOOS != runtime.GOOS || staged.Build.GOARCH != runtime.GOARCH || len(staged.Manifest.Packages) != 1 || len(staged.Manifest.StandardExports) == 0 {
		t.Fatalf("staged analyze input = %#v, %v", staged, err)
	}
	loaded, err := protocol.LoadSnapshot(staged.Manifest)
	if err != nil || len(loaded.Packages) != 1 || len(loaded.StandardExports) == 0 {
		t.Fatalf("loaded analyze input = %#v, %v", loaded, err)
	}
	foundFmt := false
	for _, item := range loaded.StandardExports {
		if item.ImportPath == "fmt" {
			foundFmt = true
			if len(item.Content) == 0 {
				t.Fatal("empty fmt export")
			}
		}
	}
	if !foundFmt {
		t.Fatal("selected fmt export is missing")
	}
	if _, err := os.Stat(filepath.Join(root, "go.sum")); !os.IsNotExist(err) {
		t.Fatalf("Project go.sum changed: %v", err)
	}
	t.Setenv("GOOS", "linux")
	t.Setenv("GOARCH", "amd64")
	t.Setenv("CGO_ENABLED", "0")
	tagged, err := project.StageAnalyzeInput(context.Background(), root, filepath.Join(t.TempDir(), "tagged-snapshot"), project.SourceOptions{Offline: true, BuildTags: []string{"customdata"}})
	if err != nil || tagged.Build.GOOS != "linux" || tagged.Build.GOARCH != "amd64" || len(tagged.Build.BuildTags) != 1 || tagged.Build.BuildTags[0] != "customdata" || len(tagged.Manifest.Packages) != 1 || len(tagged.Manifest.Packages[0].Files) != 2 {
		t.Fatalf("tagged target input = %#v, %v", tagged, err)
	}
	foundJSON := false
	for _, item := range tagged.Manifest.StandardExports {
		foundJSON = foundJSON || item.ImportPath == "encoding/json"
	}
	if !foundJSON {
		t.Fatal("tag-selected standard export is missing")
	}
	staged.Manifest.StandardExports[0].Digest = "sha256:" + strings.Repeat("0", 64)
	if _, err := protocol.LoadSnapshot(staged.Manifest); !errors.Is(err, protocol.ErrInvalidSnapshotManifest) {
		t.Fatalf("tampered standard export accepted: %v", err)
	}
}

func TestStageAnalyzeInputDoesNotReplaceSelectedToolchain(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "model/member.go", "package model\n//plystra:data example.member/v1\nvar Member = 1\n")
	t.Setenv("GOTOOLCHAIN", "go1.99.0")
	if _, err := project.StageAnalyzeInput(context.Background(), root, filepath.Join(t.TempDir(), "snapshot"), project.SourceOptions{Offline: true}); !errors.Is(err, project.ErrSourceInventory) {
		t.Fatalf("unavailable selected toolchain error = %v", err)
	}
}

func TestDiscoverSourceClosureVerifiesOrdinaryModule(t *testing.T) {
	root := t.TempDir()
	proxy := filepath.Join(root, "proxy")
	app := filepath.Join(root, "app")
	writeProjectFile(t, app, "go.mod", "module example.com/app\ngo 1.26\nrequire example.com/support v0.1.0\n")
	writeProjectFile(t, app, "plystra.yaml", "project: app\n")
	writeProjectFile(t, app, "model/member.go", "package model\nimport \"example.com/support\"\n//plystra:data example.member/v1\nvar Member = support.Value\n")
	writeProxyProject(t, proxy, "example.com/support", "v0.1.0", map[string]string{
		"go.mod":     "module example.com/support\ngo 1.26\n",
		"support.go": "package support\nconst Value = 1\n",
	})
	t.Setenv("GOPROXY", (&url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(proxy)}).String())
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GOMODCACHE", filepath.Join(root, "cache"))
	t.Setenv("GOWORK", "off")
	command := exec.Command("go", "mod", "download", "example.com/support@v0.1.0")
	command.Dir = app
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("download support: %v: %s", err, output)
	}
	result, err := project.DiscoverSourceClosure(context.Background(), app, project.SourceOptions{Offline: true})
	if err != nil || len(result.Packages) != 2 || result.Packages[1].ImportPath != "example.com/support" ||
		result.Packages[1].Module.Origin != "module" || !strings.HasPrefix(result.Packages[1].Module.Checksum, "h1:") {
		t.Fatalf("ordinary support = %#v, %v", result, err)
	}
	cachedFile := filepath.Join(root, "cache", "example.com", "support@v0.1.0", "support.go")
	if err := os.Chmod(cachedFile, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cachedFile, []byte("package support\nconst Value = 2\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := project.DiscoverSourceClosure(context.Background(), app, project.SourceOptions{Offline: true}); !errors.Is(err, project.ErrSourceInventory) {
		t.Fatalf("tampered support error = %v", err)
	}
}

func TestDiscoverSourceClosureRejectsUnresolvedImport(t *testing.T) {
	root := t.TempDir()
	writeProjectFile(t, root, "go.mod", "module example.com/app\ngo 1.26\n")
	writeProjectFile(t, root, "plystra.yaml", "project: app\n")
	writeProjectFile(t, root, "model/member.go", "package model\nimport _ \"example.com/missing\"\n//plystra:data example.member/v1\nvar Member = 1\n")
	if _, err := project.DiscoverSourceClosure(context.Background(), root, project.SourceOptions{Offline: true}); !errors.Is(err, project.ErrSourceInventory) {
		t.Fatalf("missing support error = %v", err)
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
