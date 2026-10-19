package project

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/plystra/data/analyzer"
	"github.com/plystra/data/declaration"
)

var ErrInvalidActivation = errors.New("invalid Data member activation")

// ActiveMember is one explicitly selected Data member after analysis and
// assignment closure have been checked. It does not assert backend selection.
type ActiveMember struct {
	MemberID      string `json:"member_id"`
	Resource      string `json:"resource"`
	Namespace     string `json:"namespace"`
	Access        string `json:"access,omitempty"`
	AccessPackage string `json:"access_package,omitempty"`
	AccessType    string `json:"access_type,omitempty"`
	AccessID      string `json:"access_id,omitempty"`
	MigrationOnly bool   `json:"migration_only"`
	ModelDigest   string `json:"model_digest"`
}

// ValidateActivation closes explicit assignments over validated analyze roots.
// Visible but unassigned roots remain dormant; imported roots must be assigned.
func ValidateActivation(assignments []Assignment, roots []analyzer.Root) ([]ActiveMember, error) {
	if len(assignments) > MaxMembers || len(roots) > MaxMembers {
		return nil, fmt.Errorf("%w: member limit exceeded", ErrInvalidActivation)
	}
	available := make(map[string]analyzer.Root, len(roots))
	for _, root := range roots {
		if !declaration.ValidMemberID(root.MemberID) {
			return nil, fmt.Errorf("%w: invalid analyzed member ID", ErrInvalidActivation)
		}
		if _, exists := available[root.MemberID]; exists {
			return nil, fmt.Errorf("%w: duplicate analyzed member %q", ErrInvalidActivation, root.MemberID)
		}
		available[root.MemberID] = root
	}
	configured := make(map[string]Assignment, len(assignments))
	resources := make(map[string]struct{}, len(assignments))
	for _, assignment := range assignments {
		if !declaration.ValidMemberID(assignment.MemberID) || !validInstanceName(assignment.Resource) ||
			(assignment.Access != "" && !validInstanceName(assignment.Access)) {
			return nil, fmt.Errorf("%w: malformed member assignment", ErrInvalidActivation)
		}
		if _, exists := configured[assignment.MemberID]; exists {
			return nil, fmt.Errorf("%w: duplicate assignment for %q", ErrInvalidActivation, assignment.MemberID)
		}
		configured[assignment.MemberID] = assignment
		resources[assignment.Resource] = struct{}{}
	}
	accesses := make(map[string]string, len(assignments))
	namespaces := make(map[string]string, len(assignments))
	active := make([]ActiveMember, 0, len(assignments))
	for _, assignment := range assignments {
		root, exists := available[assignment.MemberID]
		if !exists {
			return nil, fmt.Errorf("%w: assigned member %q has no accepted analyze root", ErrInvalidActivation, assignment.MemberID)
		}
		if root.Model.Namespace == "" {
			return nil, fmt.Errorf("%w: member %q has no namespace", ErrInvalidActivation, assignment.MemberID)
		}
		key := assignment.Resource + "\x00" + root.Model.Namespace
		if prior, exists := namespaces[key]; exists {
			return nil, fmt.Errorf("%w: members %q and %q claim namespace %q on Resource %q", ErrInvalidActivation, prior, assignment.MemberID, root.Model.Namespace, assignment.Resource)
		}
		namespaces[key] = assignment.MemberID
		if root.MigrationOnly {
			if assignment.Access != "" || root.AccessID != "" {
				return nil, fmt.Errorf("%w: migration-only member %q cannot select access", ErrInvalidActivation, assignment.MemberID)
			}
			if len(root.Model.Queries) != 0 || len(root.Model.Plans) != 0 {
				return nil, fmt.Errorf("%w: migration-only member %q cannot declare queries or plans", ErrInvalidActivation, assignment.MemberID)
			}
		} else {
			if assignment.Access == "" || root.AccessPackage == "" || root.AccessType == "" || root.AccessID == "" {
				return nil, fmt.Errorf("%w: member %q requires an accepted access contract and instance", ErrInvalidActivation, assignment.MemberID)
			}
			if _, collision := resources[assignment.Access]; collision || strings.HasPrefix(assignment.Access, "kernel.") {
				return nil, fmt.Errorf("%w: access instance %q collides with a Resource or reserved name", ErrInvalidActivation, assignment.Access)
			}
			if prior, exists := accesses[assignment.Access]; exists {
				return nil, fmt.Errorf("%w: access instance %q belongs to both %q and %q", ErrInvalidActivation, assignment.Access, prior, assignment.MemberID)
			}
			accesses[assignment.Access] = assignment.MemberID
		}
		for _, imported := range root.Model.Imports {
			target, exists := configured[imported]
			if !exists {
				return nil, fmt.Errorf("%w: member %q imports unassigned member %q", ErrInvalidActivation, assignment.MemberID, imported)
			}
			if target.Resource != assignment.Resource {
				return nil, fmt.Errorf("%w: member %q imports %q across Resource instances", ErrInvalidActivation, assignment.MemberID, imported)
			}
		}
		modelDigest, err := declaration.DigestModel(root.Model)
		if err != nil {
			return nil, fmt.Errorf("%w: member %q model digest: %v", ErrInvalidActivation, assignment.MemberID, err)
		}
		item := ActiveMember{MemberID: assignment.MemberID, Resource: assignment.Resource,
			Namespace: root.Model.Namespace, Access: assignment.Access,
			MigrationOnly: root.MigrationOnly, ModelDigest: modelDigest}
		if !root.MigrationOnly {
			item.AccessPackage, item.AccessType, item.AccessID = root.AccessPackage, root.AccessType, root.AccessID
		}
		active = append(active, item)
	}
	sort.Slice(active, func(i, j int) bool { return active[i].MemberID < active[j].MemberID })
	return active, nil
}
