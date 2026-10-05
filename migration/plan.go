// Package migration validates immutable migration intent and produces a
// deterministic pending order. Backend execution belongs to a provider.
package migration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	ErrInvalidDefinition  = errors.New("invalid migration definition")
	ErrDuplicateIdentity  = errors.New("duplicate migration identity")
	ErrDuplicateVersion   = errors.New("duplicate migration version")
	ErrMissingPredecessor = errors.New("missing migration predecessor")
	ErrCycle              = errors.New("migration dependency cycle")
	ErrHistoryDrift       = errors.New("migration history drift")
)

var (
	memberPattern    = regexp.MustCompile(`^[a-z][a-z0-9-]*(?:\.[a-z][a-z0-9-]*)+/v[1-9][0-9]*$`)
	migrationPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)
)

// Identity is immutable member-local migration identity.
type Identity struct {
	Member string `json:"member"`
	Name   string `json:"name"`
}

func (id Identity) String() string { return id.Member + "#" + id.Name }

func (id Identity) valid() bool {
	return memberPattern.MatchString(id.Member) && migrationPattern.MatchString(id.Name)
}

// Definition is one immutable logical migration and its selected compiled
// artifact metadata. SQL is deliberately outside the planner contract.
type Definition struct {
	Identity       Identity   `json:"identity"`
	Version        uint64     `json:"version"`
	Predecessors   []Identity `json:"predecessors,omitempty"`
	LogicalDigest  string     `json:"logical_digest"`
	ArtifactDigest string     `json:"artifact_digest"`
	Compiler       string     `json:"compiler"`
	Backend        string     `json:"backend"`
}

// HistoryEntry is the immutable evidence recorded after a migration commits.
type HistoryEntry struct {
	Identity       Identity `json:"identity"`
	Version        uint64   `json:"version"`
	LogicalDigest  string   `json:"logical_digest"`
	ArtifactDigest string   `json:"artifact_digest"`
	Compiler       string   `json:"compiler"`
	Backend        string   `json:"backend"`
}

// Request supplies the stable target and complete current migration catalog.
type Request struct {
	Target      string         `json:"target"`
	Backend     string         `json:"backend"`
	Compiler    string         `json:"compiler"`
	Definitions []Definition   `json:"definitions"`
	History     []HistoryEntry `json:"history"`
}

// Plan is a complete immutable snapshot of migration state. Digest excludes
// invocation identity and presentation fields.
type Plan struct {
	Target     string         `json:"target"`
	Backend    string         `json:"backend"`
	Compiler   string         `json:"compiler"`
	Applied    []HistoryEntry `json:"applied"`
	Pending    []Definition   `json:"pending"`
	PlanDigest string         `json:"plan_digest"`
}

// Build validates the full catalog and observed prefix, then orders all
// pending work with a stable topological sort.
func Build(request Request) (Plan, error) {
	if strings.TrimSpace(request.Target) == "" || strings.TrimSpace(request.Backend) == "" || strings.TrimSpace(request.Compiler) == "" {
		return Plan{}, fmt.Errorf("%w: target, backend, and compiler are required", ErrInvalidDefinition)
	}
	definitions := make(map[Identity]Definition, len(request.Definitions))
	versions := make(map[string]map[uint64]Identity)
	for _, definition := range request.Definitions {
		if err := validateDefinition(definition, request.Backend, request.Compiler); err != nil {
			return Plan{}, err
		}
		if _, exists := definitions[definition.Identity]; exists {
			return Plan{}, fmt.Errorf("%w: %s", ErrDuplicateIdentity, definition.Identity)
		}
		memberVersions := versions[definition.Identity.Member]
		if memberVersions == nil {
			memberVersions = make(map[uint64]Identity)
			versions[definition.Identity.Member] = memberVersions
		}
		if previous, exists := memberVersions[definition.Version]; exists {
			return Plan{}, fmt.Errorf("%w: %s and %s", ErrDuplicateVersion, previous, definition.Identity)
		}
		memberVersions[definition.Version] = definition.Identity
		definition.Predecessors = sortedIdentities(definition.Predecessors)
		definitions[definition.Identity] = cloneDefinition(definition)
	}

	graph := make(map[Identity][]Identity, len(definitions))
	for identity, definition := range definitions {
		for _, predecessor := range definition.Predecessors {
			if _, exists := definitions[predecessor]; !exists {
				return Plan{}, fmt.Errorf("%w: %s requires %s", ErrMissingPredecessor, identity, predecessor)
			}
			graph[identity] = append(graph[identity], predecessor)
		}
	}
	if err := rejectCycles(graph); err != nil {
		return Plan{}, err
	}

	applied, appliedSet, err := validateHistory(request.History, definitions, request.Backend, request.Compiler)
	if err != nil {
		return Plan{}, err
	}
	pending, err := orderPending(definitions, graph, appliedSet)
	if err != nil {
		return Plan{}, err
	}
	plan := Plan{
		Target: request.Target, Backend: request.Backend, Compiler: request.Compiler,
		Applied: cloneHistory(applied), Pending: cloneDefinitions(pending),
	}
	digest, err := digestPlan(plan)
	if err != nil {
		return Plan{}, err
	}
	plan.PlanDigest = digest
	return plan, nil
}

func validateDefinition(definition Definition, backend, compiler string) error {
	if !definition.Identity.valid() || definition.Version == 0 || definition.LogicalDigest == "" || definition.ArtifactDigest == "" || definition.Compiler == "" || definition.Backend == "" {
		return fmt.Errorf("%w: %s", ErrInvalidDefinition, definition.Identity)
	}
	if definition.Backend != backend || definition.Compiler != compiler {
		return fmt.Errorf("%w: %s compiler or backend identity", ErrHistoryDrift, definition.Identity)
	}
	seen := make(map[Identity]struct{}, len(definition.Predecessors))
	for _, predecessor := range definition.Predecessors {
		if !predecessor.valid() || predecessor == definition.Identity {
			return fmt.Errorf("%w: predecessor of %s", ErrInvalidDefinition, definition.Identity)
		}
		if _, exists := seen[predecessor]; exists {
			return fmt.Errorf("%w: duplicate predecessor of %s", ErrInvalidDefinition, definition.Identity)
		}
		seen[predecessor] = struct{}{}
	}
	return nil
}

func validateHistory(history []HistoryEntry, definitions map[Identity]Definition, backend, compiler string) ([]HistoryEntry, map[Identity]struct{}, error) {
	clone := cloneHistory(history)
	applied := make(map[Identity]struct{}, len(history))
	for index, entry := range clone {
		definition, exists := definitions[entry.Identity]
		if !exists || entry.Version != definition.Version || entry.LogicalDigest != definition.LogicalDigest || entry.ArtifactDigest != definition.ArtifactDigest || entry.Compiler != compiler || entry.Backend != backend {
			return nil, nil, fmt.Errorf("%w: history entry %d", ErrHistoryDrift, index)
		}
		if _, duplicate := applied[entry.Identity]; duplicate {
			return nil, nil, fmt.Errorf("%w: history entry %s", ErrHistoryDrift, entry.Identity)
		}
		for _, predecessor := range definition.Predecessors {
			if _, present := applied[predecessor]; !present {
				return nil, nil, fmt.Errorf("%w: applied %s precedes %s", ErrHistoryDrift, predecessor, entry.Identity)
			}
		}
		applied[entry.Identity] = struct{}{}
	}
	return clone, applied, nil
}

func orderPending(definitions map[Identity]Definition, graph map[Identity][]Identity, applied map[Identity]struct{}) ([]Definition, error) {
	remaining := make(map[Identity]struct{}, len(definitions))
	for identity := range definitions {
		if _, exists := applied[identity]; !exists {
			remaining[identity] = struct{}{}
		}
	}
	ordered := make([]Definition, 0, len(remaining))
	for len(remaining) > 0 {
		ready := make([]Identity, 0)
		for identity := range remaining {
			allApplied := true
			for _, predecessor := range graph[identity] {
				if _, done := applied[predecessor]; !done {
					if _, pending := remaining[predecessor]; pending {
						allApplied = false
						break
					}
					return nil, fmt.Errorf("%w: %s", ErrHistoryDrift, predecessor)
				}
			}
			if allApplied {
				ready = append(ready, identity)
			}
		}
		if len(ready) == 0 {
			return nil, ErrCycle
		}
		sort.Slice(ready, func(i, j int) bool { return ready[i].String() < ready[j].String() })
		for _, identity := range ready {
			ordered = append(ordered, cloneDefinition(definitions[identity]))
			applied[identity] = struct{}{}
			delete(remaining, identity)
		}
	}
	return ordered, nil
}

func rejectCycles(graph map[Identity][]Identity) error {
	state := make(map[Identity]uint8, len(graph))
	var visit func(Identity) error
	visit = func(identity Identity) error {
		switch state[identity] {
		case 1:
			return fmt.Errorf("%w: %s", ErrCycle, identity)
		case 2:
			return nil
		}
		state[identity] = 1
		for _, predecessor := range graph[identity] {
			if err := visit(predecessor); err != nil {
				return err
			}
		}
		state[identity] = 2
		return nil
	}
	for identity := range graph {
		if err := visit(identity); err != nil {
			return err
		}
	}
	return nil
}

func digestPlan(plan Plan) (string, error) {
	payload := struct {
		Target   string         `json:"target"`
		Backend  string         `json:"backend"`
		Compiler string         `json:"compiler"`
		Applied  []HistoryEntry `json:"applied"`
		Pending  []Definition   `json:"pending"`
	}{plan.Target, plan.Backend, plan.Compiler, plan.Applied, plan.Pending}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func sortedIdentities(values []Identity) []Identity {
	copyValues := append([]Identity(nil), values...)
	sort.Slice(copyValues, func(i, j int) bool { return copyValues[i].String() < copyValues[j].String() })
	return copyValues
}

func cloneDefinition(definition Definition) Definition {
	definition.Predecessors = append([]Identity(nil), definition.Predecessors...)
	return definition
}

func cloneDefinitions(definitions []Definition) []Definition {
	result := make([]Definition, len(definitions))
	for index, definition := range definitions {
		result[index] = cloneDefinition(definition)
	}
	return result
}

func cloneHistory(history []HistoryEntry) []HistoryEntry {
	return append([]HistoryEntry(nil), history...)
}
