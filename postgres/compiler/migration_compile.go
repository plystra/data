package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/plystra/data/declaration"
	"github.com/plystra/data/migration"
)

// CompileMigrations lowers immutable Data migration intent into PostgreSQL
// artifacts accepted by MigrationPlan and ApplyMigrations. Applied artifacts
// are verified and reused from history, including their original compiler.
func CompileMigrations(members []SchemaMember, compiler string, history MigrationHistory) ([]Artifact, error) {
	if strings.TrimSpace(compiler) == "" {
		return nil, fmt.Errorf("%w: compiler identity is required", ErrSchemaCompilation)
	}
	if len(members) == 0 {
		return nil, fmt.Errorf("%w: no members", ErrSchemaCompilation)
	}
	normalized := make([]SchemaMember, len(members))
	byID := make(map[string]SchemaMember, len(members))
	for index, member := range members {
		if strings.TrimSpace(member.MemberID) == "" || strings.TrimSpace(member.Resource) == "" {
			return nil, fmt.Errorf("%w: member %d identity or Resource", ErrSchemaCompilation, index)
		}
		model, err := (declaration.Member[struct{}]{
			Namespace: member.Model.Namespace, Imports: member.Model.Imports,
			Tables: member.Model.Tables, Queries: member.Model.Queries,
			Plans: member.Model.Plans, Migrations: member.Model.Migrations,
		}).Normalize()
		if err != nil {
			return nil, fmt.Errorf("%w: member %s: %v", ErrSchemaCompilation, member.MemberID, err)
		}
		if _, exists := byID[member.MemberID]; exists {
			return nil, fmt.Errorf("%w: duplicate member %s", ErrSchemaCompilation, member.MemberID)
		}
		member.Model = model
		normalized[index] = member
		byID[member.MemberID] = member
	}
	sort.Slice(normalized, func(i, j int) bool { return normalized[i].MemberID < normalized[j].MemberID })
	retained := make(map[migration.Identity]Artifact, len(history.Artifacts))
	for _, artifact := range history.Artifacts {
		identity := artifact.Definition.Identity
		if _, duplicate := retained[identity]; duplicate || strings.TrimSpace(artifact.SQL) == "" || artifact.Definition.ArtifactDigest != DigestSQL(artifact.SQL) {
			return nil, fmt.Errorf("%w: retained artifact %s is duplicate or corrupt", migration.ErrHistoryDrift, identity)
		}
		retained[identity] = artifact
	}
	for _, entry := range history.Applied {
		if _, exists := retained[entry.Identity]; !exists {
			return nil, fmt.Errorf("%w: applied artifact %s was not retained", migration.ErrHistoryDrift, entry.Identity)
		}
	}

	type pending struct {
		member    SchemaMember
		migration declaration.Migration
	}
	var pendingMigrations []pending
	for _, member := range normalized {
		for _, item := range member.Model.Migrations {
			pendingMigrations = append(pendingMigrations, pending{member: member, migration: item})
		}
	}
	sort.Slice(pendingMigrations, func(i, j int) bool {
		left, right := pendingMigrations[i], pendingMigrations[j]
		if left.member.MemberID != right.member.MemberID {
			return left.member.MemberID < right.member.MemberID
		}
		if left.migration.Version != right.migration.Version {
			return left.migration.Version < right.migration.Version
		}
		return left.migration.ID < right.migration.ID
	})

	artifacts := make([]Artifact, 0, len(pendingMigrations))
	for _, item := range pendingMigrations {
		predecessors, err := migrationPredecessors(item.member, item.migration, byID)
		if err != nil {
			return nil, err
		}
		snapshotMember, snapshotMembers, err := migrationSnapshot(item.member, item.migration, byID)
		if err != nil {
			return nil, err
		}
		logicalDigest, err := digestMigration(item.member.MemberID, item.migration)
		if err != nil {
			return nil, fmt.Errorf("%w: migration %s logical digest: %v", ErrSchemaCompilation, item.migration.ID, err)
		}
		definition := migration.Definition{
			Identity:      migration.Identity{Member: item.member.MemberID, Name: item.migration.ID},
			Version:       item.migration.Version,
			Predecessors:  predecessors,
			LogicalDigest: logicalDigest,
			Compiler:      compiler,
			Backend:       "postgres/v1",
		}
		if artifact, exists := retained[definition.Identity]; exists {
			original := artifact.Definition
			original.Predecessors = slices.Clone(original.Predecessors)
			sort.Slice(original.Predecessors, func(i, j int) bool { return original.Predecessors[i].String() < original.Predecessors[j].String() })
			if original.Version != definition.Version || original.LogicalDigest != definition.LogicalDigest || original.Backend != definition.Backend || strings.TrimSpace(original.Compiler) == "" || !slices.Equal(original.Predecessors, definition.Predecessors) {
				return nil, fmt.Errorf("%w: retained artifact %s declaration changed", migration.ErrHistoryDrift, definition.Identity)
			}
			artifacts = append(artifacts, Artifact{Definition: original, SQL: artifact.SQL})
			delete(retained, definition.Identity)
			continue
		}
		sql, err := compileMigrationSQL(snapshotMember, item.migration, snapshotMembers)
		if err != nil {
			return nil, err
		}
		definition.ArtifactDigest = DigestSQL(sql)
		artifacts = append(artifacts, Artifact{Definition: definition, SQL: sql})
	}
	if len(retained) != 0 {
		return nil, fmt.Errorf("%w: retained artifact has no authored migration", migration.ErrHistoryDrift)
	}
	definitions := make([]migration.Definition, len(artifacts))
	for index, artifact := range artifacts {
		definitions[index] = artifact.Definition
	}
	if _, err := migration.Build(migration.Request{
		Target: "postgres:compiler-validation", Backend: "postgres/v1", Compiler: compiler, Definitions: definitions, History: history.Applied,
	}); err != nil {
		return nil, fmt.Errorf("%w: migration catalog: %w", ErrSchemaCompilation, err)
	}
	return artifacts, nil
}

func migrationPredecessors(member SchemaMember, item declaration.Migration, byID map[string]SchemaMember) ([]migration.Identity, error) {
	if len(item.Predecessors) == 0 {
		return nil, nil
	}
	predecessors := make([]migration.Identity, len(item.Predecessors))
	for index, predecessor := range item.Predecessors {
		targetMember := member
		if predecessor.Member != "" {
			var exists bool
			targetMember, exists = byID[predecessor.Member]
			if !exists {
				return nil, fmt.Errorf("%w: migration %s predecessor member %s is not present", ErrSchemaCompilation, item.ID, predecessor.Member)
			}
			if targetMember.Resource != member.Resource {
				return nil, fmt.Errorf("%w: migration %s predecessor %s", ErrCrossResourceReference, item.ID, predecessor.Member)
			}
		}
		if !hasMigration(targetMember.Model, predecessor.Symbol) {
			return nil, fmt.Errorf("%w: migration %s predecessor %s is not present", ErrSchemaCompilation, item.ID, predecessor.Symbol)
		}
		predecessors[index] = migration.Identity{Member: targetMember.MemberID, Name: predecessor.Symbol}
	}
	sort.Slice(predecessors, func(i, j int) bool { return predecessors[i].String() < predecessors[j].String() })
	return predecessors, nil
}

func migrationSnapshot(member SchemaMember, item declaration.Migration, byID map[string]SchemaMember) (SchemaMember, map[string]SchemaMember, error) {
	snapshots := make(map[string]SchemaMember, len(item.Snapshot))
	for _, schema := range item.Snapshot {
		id := schema.Member
		if id == "" {
			id = member.MemberID
		}
		if _, duplicate := snapshots[id]; duplicate || schema.Member == member.MemberID {
			return SchemaMember{}, nil, fmt.Errorf("%w: duplicate or self-imported migration snapshot %s", ErrSchemaCompilation, id)
		}
		target, exists := byID[id]
		if !exists {
			return SchemaMember{}, nil, fmt.Errorf("%w: migration snapshot member %s is not active", ErrSchemaCompilation, id)
		}
		if target.Resource != member.Resource {
			return SchemaMember{}, nil, fmt.Errorf("%w: migration snapshot %s", ErrCrossResourceReference, id)
		}
		snapshots[id] = SchemaMember{MemberID: id, Resource: member.Resource, Model: declaration.Model{Namespace: schema.Namespace, Tables: schema.Tables}}
	}
	owner := snapshots[member.MemberID]
	owner.MemberID, owner.Resource = member.MemberID, member.Resource
	return owner, snapshots, nil
}

func compileMigrationSQL(member SchemaMember, item declaration.Migration, byID map[string]SchemaMember) (string, error) {
	var sql strings.Builder
	fmt.Fprintf(&sql, "-- plystra.data.postgres-migration/v1 %s#%s\n", member.MemberID, item.ID)
	for _, operation := range item.Operations {
		switch operation.Kind {
		case declaration.MigrationCreateTable:
			targetMember, table, err := resolveSchemaTable(member, operation.Table, byID)
			if err != nil {
				return "", fmt.Errorf("%w: migration %s create table: %v", ErrSchemaCompilation, item.ID, err)
			}
			fmt.Fprintf(&sql, "CREATE SCHEMA IF NOT EXISTS %s;\n", quoteIdentifier(targetMember.Model.Namespace))
			if err := writeTable(&sql, targetMember, table); err != nil {
				return "", err
			}
			for _, constraint := range table.Constraints {
				if constraint.Kind == declaration.ConstraintForeignKey {
					if err := writeForeignKey(&sql, targetMember, table, constraint, byID); err != nil {
						return "", err
					}
				}
			}
			for _, index := range table.Indexes {
				writeIndex(&sql, targetMember, table, index)
			}
		case declaration.MigrationAddColumn:
			targetMember, table, err := resolveSchemaTable(member, operation.Table, byID)
			if err != nil {
				return "", fmt.Errorf("%w: migration %s add column: %v", ErrSchemaCompilation, item.ID, err)
			}
			column, err := columnDefinition(operation.Column)
			if err != nil {
				return "", fmt.Errorf("%w: migration %s add column: %v", ErrSchemaCompilation, item.ID, err)
			}
			fmt.Fprintf(&sql, "ALTER TABLE %s.%s ADD COLUMN %s;\n", quoteIdentifier(targetMember.Model.Namespace), quoteIdentifier(table.ID), column)
		case declaration.MigrationCreateIndex:
			targetMember, table, err := resolveSchemaTable(member, operation.Table, byID)
			if err != nil {
				return "", fmt.Errorf("%w: migration %s create index: %v", ErrSchemaCompilation, item.ID, err)
			}
			if err := validateIndexColumns(table, operation.Index); err != nil {
				return "", fmt.Errorf("%w: migration %s create index: %v", ErrSchemaCompilation, item.ID, err)
			}
			writeIndex(&sql, targetMember, table, operation.Index)
		default:
			return "", fmt.Errorf("%w: migration %s operation %q", ErrSchemaCompilation, item.ID, operation.Kind)
		}
	}
	return sql.String(), nil
}

func columnDefinition(column declaration.Column) (string, error) {
	columnType, err := postgresType(column.Type)
	if err != nil {
		return "", err
	}
	result := quoteIdentifier(column.ID) + " " + columnType
	if !column.Nullable {
		result += " NOT NULL"
	}
	if column.Default.Set {
		value, err := defaultSQL(column.Type, column.Default)
		if err != nil {
			return "", err
		}
		result += " DEFAULT " + value
	}
	return result, nil
}

func validateIndexColumns(table declaration.Table, index declaration.Index) error {
	if index.ID == "" || len(index.Columns) == 0 {
		return fmt.Errorf("index identity or columns are missing")
	}
	for _, column := range index.Columns {
		if !hasColumn(table, column) {
			return fmt.Errorf("index column %s is not present", column)
		}
	}
	return nil
}

func hasMigration(model declaration.Model, name string) bool {
	for _, item := range model.Migrations {
		if item.ID == name {
			return true
		}
	}
	return false
}

func digestMigration(memberID string, item declaration.Migration) (string, error) {
	payload, err := json.Marshal(struct {
		Member    string                `json:"member"`
		Migration declaration.Migration `json:"migration"`
	}{Member: memberID, Migration: item})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("plystra.data.logical-migration/v1\x00"), payload...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
