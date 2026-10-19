package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/plystra/data/project"
)

func TestInspectAssignmentsReadsProjectWithoutMutation(t *testing.T) {
	root := t.TempDir()
	content := []byte("data:\n  members:\n    authz.persistence/v1: {resource: database.primary}\n    authn.persistence/v1: {resource: database.primary, access: authn.persistence}\n")
	path := filepath.Join(root, configName)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"inspect", "assignments", "--project", root, "--format", "json"}, &stdout, &stderr); code != 0 || stderr.Len() != 0 {
		t.Fatalf("inspect exit = %d, stderr = %q", code, stderr.String())
	}
	var output struct {
		Schema  string               `json:"schema"`
		Members []project.Assignment `json:"members"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &output); err != nil {
		t.Fatal(err)
	}
	want := []project.Assignment{{MemberID: "authn.persistence/v1", Resource: "database.primary", Access: "authn.persistence"}, {MemberID: "authz.persistence/v1", Resource: "database.primary"}}
	if output.Schema != "plystra.data-assignments/v1" || !reflect.DeepEqual(output.Members, want) {
		t.Fatalf("inspection = %#v", output)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, content) {
		t.Fatalf("Project configuration changed: %v", err)
	}
	stdout.Reset()
	if code := run([]string{"inspect", "assignments", "--project", root}, &stdout, &stderr); code != 0 || !strings.HasPrefix(stdout.String(), "authn.persistence/v1  database.primary  authn.persistence\n") {
		t.Fatalf("text inspection = %d, %q", code, stdout.String())
	}
}

func TestInspectAssignmentsRejectsInvalidProjectInput(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"unknown command", []string{"generate"}, "usage:"},
		{"invalid format", []string{"inspect", "assignments", "--project", root, "--format", "yaml"}, "expected --format"},
		{"missing file", []string{"inspect", "assignments", "--project", root}, "cannot read plystra.data.yaml"},
		{"missing directory", []string{"inspect", "assignments", "--project", filepath.Join(root, "missing")}, "Project directory is unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("inspect = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
			}
		})
	}
	if err := os.WriteFile(filepath.Join(root, configName), []byte("data:\n  members:\n    invalid: {resource: database.primary}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"inspect", "assignments", "--project", root}, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "line 3 column 5") {
		t.Fatalf("invalid configuration = %d, %q", code, stderr.String())
	}
}

func TestCheckAssignmentsRejectsInvalidProjectInput(t *testing.T) {
	root := t.TempDir()
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"invalid format", []string{"check", "assignments", "--project", root, "--format", "yaml"}, "expected --format"},
		{"positional argument", []string{"check", "assignments", "extra"}, "no positional arguments"},
		{"missing file", []string{"check", "assignments", "--project", root}, "cannot read plystra.data.yaml"},
		{"missing directory", []string{"check", "assignments", "--project", filepath.Join(root, "missing")}, "Project directory is unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := run(test.args, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("check = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
			}
		})
	}
	if err := os.WriteFile(filepath.Join(root, configName), []byte("data:\n  members:\n    invalid: {resource: database.primary}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if code := run([]string{"check", "assignments", "--project", root}, &stdout, &stderr); code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "line 3 column 5") {
		t.Fatalf("invalid configuration = %d, %q", code, stderr.String())
	}
}

func TestPublicInspectAssignmentsCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, configName), []byte("data:\n  members:\n    authn.persistence/v1: {resource: database.primary, access: authn.persistence}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".", "inspect", "assignments", "--project", root, "--format", "json")
	command.Env = append(os.Environ(), "GOWORK=off")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 || !strings.Contains(stdout.String(), `"schema":"plystra.data-assignments/v1"`) {
		t.Fatalf("public command = %v, stdout = %q, stderr = %q", err, stdout.String(), stderr.String())
	}
}

func TestPublicInspectSourcesCommand(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":          "module example.com/app\ngo 1.26\n",
		"plystra.yaml":    "project: app\n",
		"model/member.go": "package model\n//plystra:data accounts.user/v1\nvar Member = 1\n",
		"model/helper.go": "package model\nvar Helper = 2\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".", "inspect", "sources", "--project", root, "--format", "json", "--offline")
	command.Env = append(os.Environ(), "GOWORK=off")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("public source inspection = %v, stderr = %q", err, stderr.String())
	}
	var result struct {
		Schema   string `json:"schema"`
		Packages []struct {
			ImportPath string `json:"import_path"`
			Module     struct {
				Path   string `json:"path"`
				Origin string `json:"origin"`
			} `json:"module"`
			Files []struct {
				Path   string `json:"path"`
				Digest string `json:"digest"`
			} `json:"files"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Schema != "plystra.data-source-inventory/v1" || len(result.Packages) != 1 || result.Packages[0].ImportPath != "example.com/app/model" || result.Packages[0].Module.Path != "example.com/app" || result.Packages[0].Module.Origin != "current" || len(result.Packages[0].Files) != 2 {
		t.Fatalf("source inspection = %#v, %v", result, err)
	}
	if strings.Contains(stdout.String(), "var Member") || !strings.HasPrefix(result.Packages[0].Files[0].Digest, "sha256:") {
		t.Fatalf("source inspection exposed content or omitted digest: %q", stdout.String())
	}
}

func TestPublicInspectSourcesClosureCommand(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":             "module example.com/app\ngo 1.26\n",
		"plystra.yaml":       "project: app\n",
		"model/member.go":    "package model\nimport \"example.com/app/support\"\n//plystra:data accounts.user/v1\nvar Member = support.Value\n",
		"support/support.go": "package support\nconst Value = 1\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".", "inspect", "sources", "--project", root, "--format", "json", "--closure", "--offline")
	command.Env = append(os.Environ(), "GOWORK=off")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("public closure inspection = %v, stderr = %q", err, stderr.String())
	}
	var result struct {
		Packages []struct {
			ImportPath  string `json:"import_path"`
			Eligibility string `json:"root_eligibility"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || len(result.Packages) != 2 ||
		result.Packages[0].ImportPath != "example.com/app/model" || result.Packages[0].Eligibility != "eligible" ||
		result.Packages[1].ImportPath != "example.com/app/support" || result.Packages[1].Eligibility != "support" {
		t.Fatalf("closure output = %#v, %v", result, err)
	}
}

func TestPublicInspectInputsCommand(t *testing.T) {
	root := t.TempDir()
	for name, content := range map[string]string{
		"go.mod":             "module example.com/app\ngo 1.26\n",
		"plystra.yaml":       "project: app\n",
		"model/member.go":    "package model\nimport (\"fmt\"; \"example.com/app/access\")\n//plystra:data accounts.user/v1\nvar Member = fmt.Sprint(missingAuthoredName)\nvar Contract access.Resource\n",
		"access/resource.go": "package access\n//plystra:resource accounts.access/v1\ntype Resource interface { Read() error }\n",
	} {
		path := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "go", "run", ".", "inspect", "inputs", "--project", root, "--format", "json", "--offline")
	command.Env = append(os.Environ(), "GOWORK=off")
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	if err := command.Run(); err != nil || stderr.Len() != 0 {
		t.Fatalf("public input inspection = %v, stderr = %q", err, stderr.String())
	}
	var result struct {
		Schema          string `json:"schema"`
		GoToolchain     string `json:"go_toolchain"`
		SnapshotDigest  string `json:"snapshot_digest"`
		SourcePackages  int    `json:"source_packages"`
		StandardExports []struct {
			ImportPath string `json:"import_path"`
			Digest     string `json:"digest"`
			Bytes      int    `json:"bytes"`
		} `json:"standard_exports"`
		Resources []struct {
			ImportPath string `json:"import_path"`
			TypeName   string `json:"type_name"`
			ID         string `json:"id"`
		} `json:"resources"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil || result.Schema != "plystra.data-input-inspection/v1" || result.GoToolchain == "" || !strings.HasPrefix(result.SnapshotDigest, "sha256:") || result.SourcePackages != 2 || len(result.StandardExports) == 0 || len(result.Resources) != 1 || result.Resources[0].ImportPath != "example.com/app/access" || result.Resources[0].TypeName != "Resource" || result.Resources[0].ID != "accounts.access/v1" {
		t.Fatalf("input inspection = %#v, %v", result, err)
	}
	foundFmt := false
	for _, item := range result.StandardExports {
		if item.ImportPath == "fmt" {
			foundFmt = item.Bytes > 0 && strings.HasPrefix(item.Digest, "sha256:")
		}
	}
	if !foundFmt || strings.Contains(stdout.String(), "missingAuthoredName") || strings.Contains(stdout.String(), "go-build") || strings.Contains(stdout.String(), root) {
		t.Fatalf("input inspection omitted fmt or exposed source/cache path: %q", stdout.String())
	}
	if _, err := os.Stat(filepath.Join(root, "go.sum")); !os.IsNotExist(err) {
		t.Fatalf("Project go.sum changed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "access", "resource.go"), []byte("package access\n//plystra:resource invalid\ntype Resource interface { Read() error }\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stdout.Reset()
	stderr.Reset()
	if code := run([]string{"inspect", "inputs", "--project", root, "--format", "json", "--offline"}, &stdout, &stderr); code != 2 || stdout.Len() != 0 ||
		!strings.Contains(stderr.String(), "invalid selected Resource contract") || !strings.Contains(stderr.String(), "access/resource.go") || strings.Contains(stderr.String(), root) {
		t.Fatalf("malformed Resource inspection = %d, stdout = %q, stderr = %q", code, stdout.String(), stderr.String())
	}
}

func TestInspectAssignmentsReportsOutputFailure(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, configName), []byte("data:\n  members: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run([]string{"inspect", "assignments", "--project", root, "--format", format}, failedWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "cannot write inspection output") {
				t.Fatalf("output failure = %d, %q", code, stderr.String())
			}
		})
	}
}

type failedWriter struct{}

func (failedWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }
