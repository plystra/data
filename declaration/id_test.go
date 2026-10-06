package declaration_test

import (
	"strings"
	"testing"

	"github.com/plystra/data/declaration"
)

func TestValidMemberIDUsesVersionedSemanticGrammarWithoutSymbolLimit(t *testing.T) {
	t.Parallel()
	for _, value := range []string{
		"accounts.user/v1",
		strings.Repeat("a", 124) + ".b/v1",
	} {
		if !declaration.ValidMemberID(value) {
			t.Fatalf("valid member ID %q was rejected", value)
		}
	}
	for _, value := range []string{
		"accounts.user/v0",
		"accounts.user/v01",
		"accounts.user/v18446744073709551616",
		"accounts.user/v1/other",
	} {
		if declaration.ValidMemberID(value) {
			t.Fatalf("invalid member ID %q was accepted", value)
		}
	}
}
