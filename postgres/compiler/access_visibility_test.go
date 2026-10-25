package compiler_test

import (
	"errors"
	"testing"

	"github.com/plystra/data/analyzer"
	postgres "github.com/plystra/data/postgres/compiler"
)

func TestCompileAccessProvidersRejectRestrictedImportPaths(t *testing.T) {
	for _, restricted := range []string{
		"example.com/app/auth/internal/access",
		"example.com/app/internal/access",
		"example.com/app/vendor/access",
		"example.com/app/internal",
		"example.com/app/vendor",
		"internal/access",
		"vendor/access",
	} {
		for _, field := range []string{"contract", "request", "row"} {
			t.Run(restricted+"/"+field, func(t *testing.T) {
				member, root := accessFixture(t)
				switch field {
				case "contract":
					root.AccessPackage = restricted
				case "request":
					root.QueryMethods[0].RequestPackage = restricted
				case "row":
					root.QueryMethods[0].RowPackage = restricted
				}
				artifacts, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root})
				if !errors.Is(err, postgres.ErrAccessCompilation) || len(artifacts) != 0 {
					t.Fatalf("restricted import accepted: artifacts=%d err=%v", len(artifacts), err)
				}
			})
		}
	}
}

func TestCompileAccessProvidersKeepPublicPackageNames(t *testing.T) {
	member, root := accessFixture(t)
	root.AccessPackage = "example.com/internalized/vendor-tools/access"
	root.QueryMethods[0].RequestPackage = "example.com/app/internalized/request"
	root.QueryMethods[0].RowPackage = "example.com/app/vendor-tools/row"
	if artifacts, err := postgres.CompileAccessProviders([]postgres.SchemaMember{member}, []analyzer.Root{root}); err != nil || len(artifacts) != 1 {
		t.Fatalf("public package rejected: artifacts=%d err=%v", len(artifacts), err)
	}
}
