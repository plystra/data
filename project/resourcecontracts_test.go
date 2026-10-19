package project

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/plystra/data/analyzer"
)

func TestSelectedResourceContractsFromVerifiedClosure(t *testing.T) {
	packages := []SourcePackage{
		resourcePackage("example.com/app/z", "z/resource.go", `package z
type Other struct{}
//plystra:resource audit.log/v1
type Resource interface { Write() error }
`),
		resourcePackage("example.com/app/a", "a/resource.go", `package a
type (
    //plystra:resource accounts.store/v1
    Resource interface { Read() error }
)
`),
	}
	got, err := selectedResourceContracts(packages, []string{"example.com/app"})
	if err != nil {
		t.Fatal(err)
	}
	want := []analyzer.ResourceContract{
		{ImportPath: "example.com/app/a", TypeName: "Resource", ID: "accounts.store/v1"},
		{ImportPath: "example.com/app/z", TypeName: "Resource", ID: "audit.log/v1"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("contracts = %#v, want %#v", got, want)
	}
}

func TestSelectedResourceContractsRejectsInvalidDirectives(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{"malformed ID", "package access\n//plystra:resource invalid\ntype Resource interface{}\n"},
		{"wrong type", "package access\n//plystra:resource accounts.store/v1\ntype Store interface{}\n"},
		{"not interface", "package access\n//plystra:resource accounts.store/v1\ntype Resource struct{}\n"},
		{"alias", "package access\ntype Other interface{}\n//plystra:resource accounts.store/v1\ntype Resource = Other\n"},
		{"unbound", "package access\n//plystra:resource accounts.store/v1\nfunc New() {}\n"},
		{"group directive", "package access\n//plystra:resource accounts.store/v1\ntype (Resource interface{}; Other struct{})\n"},
		{"duplicate directive", "package access\n//plystra:resource accounts.store/v1\n//plystra:resource accounts.other/v1\ntype Resource interface{}\n"},
		{"Resource and Interface", "package access\n//plystra:resource accounts.store/v1\n//plystra:interface accounts.store/v1\ntype Resource interface{}\n"},
		{"trailing directive", "package access\ntype Resource interface{} //plystra:resource accounts.store/v1\n"},
		{"invalid directive source", "package access\n//plystra:resource accounts.store/v1\ntype Resource interface{\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := selectedResourceContracts([]SourcePackage{resourcePackage("example.com/app/access", "access/resource.go", test.source)}, []string{"example.com/app"})
			if !errors.Is(err, ErrInvalidResourceContract) {
				t.Fatalf("error = %v, want Resource contract failure", err)
			}
		})
	}
	_, err := selectedResourceContracts([]SourcePackage{
		resourcePackage("example.com/app/one", "one/resource.go", "package one\n//plystra:resource accounts.store/v1\ntype Resource interface{}\n"),
		resourcePackage("example.com/app/two", "two/resource.go", "package two\n//plystra:resource accounts.store/v1\ntype Resource interface{}\n"),
	}, []string{"example.com/app"})
	if !errors.Is(err, ErrInvalidResourceContract) {
		t.Fatalf("duplicate ID error = %v", err)
	}
	if !strings.Contains(err.Error(), "example.com/app/one:one/resource.go") || !strings.Contains(err.Error(), "example.com/app/two:two/resource.go") {
		t.Fatalf("duplicate ID error omitted source locations: %v", err)
	}
	contracts, err := selectedResourceContracts([]SourcePackage{resourcePackage("example.com/app/access", "access/broken.go", "package access\ntype Broken interface{\n")}, []string{"example.com/app"})
	if err != nil || len(contracts) != 0 {
		t.Fatalf("unrelated authored syntax error was preempted: %#v, %v", contracts, err)
	}
	thirdParty := resourcePackage("example.com/library/access", "access/resource.go", "package access\n//plystra:resource accounts.store/v1\ntype Resource interface{}\n")
	thirdParty.Module.Path = "example.com/library"
	contracts, err = selectedResourceContracts([]SourcePackage{thirdParty}, []string{"example.com/app"})
	if err != nil || len(contracts) != 0 {
		t.Fatalf("ordinary Go module contributed a Resource contract: %#v, %v", contracts, err)
	}
}

func resourcePackage(importPath, filePath, source string) SourcePackage {
	return SourcePackage{Package: analyzer.Package{
		ImportPath: importPath,
		Files:      []analyzer.SourceFile{{Path: filePath, Content: []byte(source)}},
	}, Module: ModuleSource{Path: "example.com/app"}}
}
