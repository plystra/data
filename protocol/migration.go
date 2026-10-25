package protocol

import (
	"fmt"

	postgres "github.com/plystra/data/postgres/compiler"
)

func emitHistory(request EmitRequest, resource string) postgres.MigrationHistory {
	for _, item := range request.MigrationHistory {
		if item.Resource == resource {
			return item.History
		}
	}
	return postgres.MigrationHistory{}
}

func validateEmitMigrationHistory(request EmitRequest) error {
	if len(request.MigrationHistory) > 1024 {
		return fmt.Errorf("migration history Resource limit exceeded")
	}
	assignments := make(map[string]string, len(request.Assignments))
	resources := map[string]bool{}
	for _, assignment := range request.Assignments {
		assignments[assignment.MemberID] = assignment.Resource
		resources[assignment.Resource] = true
	}
	seen := map[string]bool{}
	count := 0
	for _, item := range request.MigrationHistory {
		if !resources[item.Resource] || seen[item.Resource] {
			return fmt.Errorf("migration history Resource is inactive or duplicate")
		}
		seen[item.Resource] = true
		count += len(item.History.Artifacts) + len(item.History.Applied)
		if count > MaxArtifacts {
			return fmt.Errorf("migration history entry limit exceeded")
		}
		for _, artifact := range item.History.Artifacts {
			if assignments[artifact.Definition.Identity.Member] != item.Resource || len(artifact.SQL) == 0 || len(artifact.SQL) > MaxArtifactBytes {
				return fmt.Errorf("retained migration ownership or byte limit")
			}
		}
		for _, entry := range item.History.Applied {
			if assignments[entry.Identity.Member] != item.Resource {
				return fmt.Errorf("observed migration ownership")
			}
		}
	}
	return nil
}
