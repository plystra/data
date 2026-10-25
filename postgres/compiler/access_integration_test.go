//go:build integration

package compiler_test

import (
	"os"
	"testing"
)

func TestCompileAccessProvidersPostgresIntegration(t *testing.T) {
	if os.Getenv("PLYSTRA_RUN_POSTGRES_INTEGRATION") != "1" {
		t.Skip("requires disposable PostgreSQL")
	}
	directory := writeAccessFixture(t)
	for _, race := range []bool{false, true} {
		args := []string{"test", "-mod=mod", "-tags=integration"}
		if race {
			args = append(args, "-race")
		}
		args = append(args, "./providers/accounts.user/v1", "-run", "^TestGeneratedProviderPostgres$", "-count=1", "-timeout=60s")
		runAccessFixture(t, directory, args...)
	}
}
