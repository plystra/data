package project

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"

	"github.com/plystra/data/declaration"
	"go.yaml.in/yaml/v3"
)

const (
	MaxAssignmentDocumentBytes = 1 << 20
	MaxMembers                 = 1024
)

var ErrInvalidAssignments = errors.New("invalid Data member assignments")

var instanceName = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*(?:\.[a-z][a-z0-9]*(?:-[a-z0-9]+)*)*$`)

// Assignment is one explicit Data member activation in plystra.data.yaml.
type Assignment struct {
	MemberID string `json:"member_id"`
	Resource string `json:"resource"`
	Access   string `json:"access,omitempty"`
}

// ParseAssignments accepts only the closed Data-owned Project document.
func ParseAssignments(content []byte) ([]Assignment, error) {
	if len(content) > MaxAssignmentDocumentBytes {
		return nil, fmt.Errorf("%w: document exceeds %d bytes", ErrInvalidAssignments, MaxAssignmentDocumentBytes)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("%w: YAML: %v", ErrInvalidAssignments, err)
	}
	var second yaml.Node
	if err := decoder.Decode(&second); err != io.EOF {
		if err != nil {
			return nil, fmt.Errorf("%w: YAML: %v", ErrInvalidAssignments, err)
		}
		return nil, fmt.Errorf("%w: multiple YAML documents", ErrInvalidAssignments)
	}
	if document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, fmt.Errorf("%w: expected one mapping document", ErrInvalidAssignments)
	}
	root, err := closedMapping(document.Content[0], "document", "data")
	if err != nil {
		return nil, err
	}
	data, exists := root["data"]
	if !exists {
		return nil, fmt.Errorf("%w: missing data mapping", ErrInvalidAssignments)
	}
	dataFields, err := closedMapping(data, "data", "members")
	if err != nil {
		return nil, err
	}
	members, exists := dataFields["members"]
	if !exists {
		return nil, fmt.Errorf("%w: missing data.members mapping", ErrInvalidAssignments)
	}
	if members.Kind != yaml.MappingNode || members.Tag != "!!map" || members.Anchor != "" || len(members.Content)%2 != 0 {
		return nil, locationError(members, "data.members must be a mapping")
	}
	if len(members.Content)/2 > MaxMembers {
		return nil, locationError(members, fmt.Sprintf("data.members exceeds %d entries", MaxMembers))
	}
	assignments := make([]Assignment, 0, len(members.Content)/2)
	seenMembers := make(map[string]struct{}, len(members.Content)/2)
	seenAccess := make(map[string]struct{}, len(members.Content)/2)
	resources := make(map[string]struct{}, len(members.Content)/2)
	for index := 0; index < len(members.Content); index += 2 {
		key, value := members.Content[index], members.Content[index+1]
		memberID, err := stringScalar(key, "data member ID")
		if err != nil {
			return nil, err
		}
		if !declaration.ValidMemberID(memberID) {
			return nil, locationError(key, "invalid Data member ID")
		}
		if _, exists := seenMembers[memberID]; exists {
			return nil, locationError(key, "duplicate Data member ID")
		}
		seenMembers[memberID] = struct{}{}
		fields, err := closedMapping(value, "data.members entry", "resource", "access")
		if err != nil {
			return nil, err
		}
		resourceNode, exists := fields["resource"]
		if !exists {
			return nil, locationError(value, "member assignment requires resource")
		}
		resource, err := stringScalar(resourceNode, "resource")
		if err != nil {
			return nil, err
		}
		if !validInstanceName(resource) {
			return nil, locationError(resourceNode, "invalid Resource instance name")
		}
		assignment := Assignment{MemberID: memberID, Resource: resource}
		resources[resource] = struct{}{}
		if accessNode, exists := fields["access"]; exists {
			access, err := stringScalar(accessNode, "access")
			if err != nil {
				return nil, err
			}
			if !validInstanceName(access) {
				return nil, locationError(accessNode, "invalid access Resource instance name")
			}
			if _, exists := seenAccess[access]; exists {
				return nil, locationError(accessNode, "duplicate access Resource instance name")
			}
			seenAccess[access] = struct{}{}
			assignment.Access = access
		}
		assignments = append(assignments, assignment)
	}
	for access := range seenAccess {
		if _, exists := resources[access]; exists {
			return nil, fmt.Errorf("%w: access collides with an assigned database Resource instance", ErrInvalidAssignments)
		}
	}
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].MemberID < assignments[j].MemberID })
	return assignments, nil
}

func closedMapping(node *yaml.Node, name string, allowed ...string) (map[string]*yaml.Node, error) {
	if node.Kind != yaml.MappingNode || node.Tag != "!!map" || node.Anchor != "" || len(node.Content)%2 != 0 {
		return nil, locationError(node, name+" must be a mapping")
	}
	valid := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		valid[key] = struct{}{}
	}
	fields := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		keyNode, valueNode := node.Content[index], node.Content[index+1]
		key, err := stringScalar(keyNode, name+" field")
		if err != nil {
			return nil, err
		}
		if _, exists := valid[key]; !exists {
			return nil, locationError(keyNode, "unknown "+name+" field")
		}
		if _, exists := fields[key]; exists {
			return nil, locationError(keyNode, "duplicate "+name+" field")
		}
		fields[key] = valueNode
	}
	return fields, nil
}

func stringScalar(node *yaml.Node, name string) (string, error) {
	if node.Kind != yaml.ScalarNode || node.Tag != "!!str" || node.Anchor != "" {
		return "", locationError(node, name+" must be a string")
	}
	return node.Value, nil
}

func validInstanceName(value string) bool {
	return len(value) != 0 && len(value) <= 128 && instanceName.MatchString(value)
}

func locationError(node *yaml.Node, message string) error {
	return fmt.Errorf("%w: line %d column %d: %s", ErrInvalidAssignments, node.Line, node.Column, message)
}
