package protocol_test

import (
	"testing"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/protocol"
)

func TestEmitRejectsRestrictedAccessImportsWithoutArtifacts(t *testing.T) {
	for _, field := range []string{"contract", "request", "row"} {
		t.Run(field, func(t *testing.T) {
			request := typedAccessEmitRequest(t)
			root := &request.Analyze.Roots[0]
			const restricted = "example.com/app/auth/internal/access"
			switch field {
			case "contract":
				root.AccessPackage = restricted
			case "request":
				root.QueryMethods[0].RequestPackage = restricted
			case "row":
				root.QueryMethods[0].RowPackage = restricted
			}
			var err error
			request.Analyze.Digest, err = analyzer.ResultDigest(request.Analyze.Roots, nil, false)
			if err != nil {
				t.Fatal(err)
			}
			request.AnalyzeDigest = request.Analyze.Digest
			refreshEmitDigest(t, &request)
			response := protocol.HandleEmit(request)
			if response.Status != protocol.StatusRejected || len(response.Output.Artifacts) != 0 || len(response.Diagnostics) != 1 || response.Diagnostics[0].Code != analyzer.CodeEmitBackend {
				t.Fatalf("restricted access import escaped emit rejection: %#v", response)
			}
		})
	}
}
