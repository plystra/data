package project

import (
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"
)

var ErrDatabaseSelection = errors.New("invalid Data database selection")

const (
	postgresProvider = "github.com/plystra/data/postgres.New"
	databaseResource = "data.database/v1"
	postgresBackend  = "postgres/v1"
)

// ConfiguredResource is one generic Core CLI selection fact. It is not a
// resolved binding or proof that the complete application can run.
type ConfiguredResource struct {
	Name            string `json:"name"`
	Provider        string `json:"provider"`
	SourcePath      string `json:"source_path"`
	SourceLine      int    `json:"source_line"`
	SourceColumn    int    `json:"source_column"`
	ProviderVisible bool   `json:"provider_visible"`
	ResourceID      string `json:"resource_id,omitempty"`
}

type ConfiguredResources struct {
	ModulePath string               `json:"module_path"`
	Instances  []ConfiguredResource `json:"instances"`
}

type DatabaseSelection struct {
	Resource string `json:"resource"`
	Provider string `json:"provider"`
	Backend  string `json:"backend"`
}

// ValidateDatabaseSelections binds active Data members to visible, supported
// database providers without requiring generated access providers to exist.
func ValidateDatabaseSelections(active []ActiveMember, expectedModule string, configured ConfiguredResources) ([]DatabaseSelection, error) {
	if expectedModule == "" || configured.ModulePath != expectedModule || len(active) > MaxMembers || len(configured.Instances) > 4096 {
		return nil, fmt.Errorf("%w: Project identity or selection bounds are invalid", ErrDatabaseSelection)
	}
	byName := make(map[string]ConfiguredResource, len(configured.Instances))
	previous := ""
	for _, instance := range configured.Instances {
		if !validInstanceName(instance.Name) || instance.Name <= previous || instance.Provider == "" || len(instance.Provider) > 1024 ||
			instance.SourcePath == "" || path.IsAbs(instance.SourcePath) || path.Clean(instance.SourcePath) != instance.SourcePath ||
			instance.SourcePath == ".." || strings.HasPrefix(instance.SourcePath, "../") || strings.ContainsAny(instance.SourcePath, "\\:") ||
			instance.SourceLine < 1 || instance.SourceColumn < 1 ||
			(instance.ProviderVisible && instance.ResourceID == "") || (!instance.ProviderVisible && instance.ResourceID != "") {
			return nil, fmt.Errorf("%w: malformed configured Resource fact", ErrDatabaseSelection)
		}
		byName[instance.Name] = instance
		previous = instance.Name
	}
	selected := make(map[string]DatabaseSelection)
	for _, member := range active {
		if !validInstanceName(member.Resource) {
			return nil, fmt.Errorf("%w: member %q has an invalid Resource name", ErrDatabaseSelection, member.MemberID)
		}
		instance, exists := byName[member.Resource]
		if !exists {
			return nil, fmt.Errorf("%w: member %q names missing database instance %q", ErrDatabaseSelection, member.MemberID, member.Resource)
		}
		if !instance.ProviderVisible {
			return nil, fmt.Errorf("%w: member %q selects database provider %q that is not visible", ErrDatabaseSelection, member.MemberID, instance.Provider)
		}
		if instance.ResourceID != databaseResource || instance.Provider != postgresProvider {
			return nil, fmt.Errorf("%w: member %q requires the selected PostgreSQL database provider", ErrDatabaseSelection, member.MemberID)
		}
		selected[member.Resource] = DatabaseSelection{Resource: member.Resource, Provider: instance.Provider, Backend: postgresBackend}
	}
	result := make([]DatabaseSelection, 0, len(selected))
	for _, selection := range selected {
		result = append(result, selection)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Resource < result[j].Resource })
	return result, nil
}
