package compiler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/plystra/data/declaration"
	"github.com/plystra/data/migration"
)

// CompileMigrations lowers immutable Data migration intent into PostgreSQL
// artifacts accepted by MigrationPlan and ApplyMigrations.
func CompileMigrations(members []SchemaMember, compiler string) ([]Artifact, error) {
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
		sql, predecessors, err := compileMigrationSQL(item.member, item.migration, byID)
		if err != nil {
			return nil, err
		}
		logicalDigest, err := digestMigration(item.member.MemberID, item.migration)
		if err != nil {
			return nil, fmt.Errorf("%w: migration %s logical digest: %v", ErrSchemaCompilation, item.migration.ID, err)
		}
		definition := migration.Definition{
			Identity:       migration.Identity{Member: item.member.MemberID, Name: item.migration.ID},
			Version:        item.migration.Version,
			Predecessors:   predecessors,
			LogicalDigest:  logicalDigest,
			ArtifactDigest: DigestSQL(sql),
			Compiler:       compiler,
			Backend:        "postgres/v1",
		}
		artifacts = append(artifacts, Artifact{Definition: definition, SQL: sql})
	}
	definitions := make([]migration.Definition, len(artifacts))
	for index, artifact := range artifacts {
		definitions[index] = artifact.Definition
	}
	if _, err := migration.Build(migration.Request{
		Target: "postgres:compiler-validation", Backend: "postgres/v1", Compiler: compiler, Definitions: definitions,
	}); err != nil {
		return nil, fmt.Errorf("%w: migration catalog: %w", ErrSchemaCompilation, err)
	}
	return artifacts, nil
}

func compileMigrationSQL(member SchemaMember, item declaration.Migration, byID map[string]SchemaMember) (string, []migration.Identity, error) {
	predecessors := make([]migration.Identity, len(item.Predecessors))
	for index, predecessor := range item.Predecessors {
		targetMember := member
		if predecessor.Member != "" {
			var exists bool
			targetMember, exists = byID[predecessor.Member]
			if !exists {
				return "", nil, fmt.Errorf("%w: migration %s predecessor member %s is not present", ErrSchemaCompilation, item.ID, predecessor.Member)
			}
			if targetMember.Resource != member.Resource {
				return "", nil, fmt.Errorf("%w: migration %s predecessor %s", ErrCrossResourceReference, item.ID, predecessor.Member)
			}
		}
		if !hasMigration(targetMember.Model, predecessor.Symbol) {
			return "", nil, fmt.Errorf("%w: migration %s predecessor %s is not present", ErrSchemaCompilation, item.ID, predecessor.Symbol)
		}
		predecessors[index] = migration.Identity{Member: targetMember.MemberID, Name: predecessor.Symbol}
	}
	sort.Slice(predecessors, func(i, j int) bool { return predecessors[i].String() < predecessors[j].String() })

	var sql strings.Builder
	fmt.Fprintf(&sql, "-- plystra.data.postgres-migration/v1 %s#%s\n", member.MemberID, item.ID)
	for _, operation := range item.Operations {
		switch operation.Kind {
		case declaration.MigrationCreateTable:
			targetMember, table, err := resolveSchemaTable(member, operation.Table, byID)
			if err != nil {
				return "", nil, fmt.Errorf("%w: migration %s create table: %v", ErrSchemaCompilation, item.ID, err)
			}
			fmt.Fprintf(&sql, "CREATE SCHEMA IF NOT EXISTS %s;\n", quoteIdentifier(targetMember.Model.Namespace))
			if err := writeTable(&sql, targetMember, table); err != nil {
				return "", nil, err
			}
			for _, constraint := range table.Constraints {
				if constraint.Kind == declaration.ConstraintForeignKey {
					if err := writeForeignKey(&sql, targetMember, table, constraint, byID); err != nil {
						return "", nil, err
					}
				}
			}
		case declaration.MigrationAddColumn:
			targetMember, table, err := resolveSchemaTable(member, operation.Table, byID)
			if err != nil {
				return "", nil, fmt.Errorf("%w: migration %s add column: %v", ErrSchemaCompilation, item.ID, err)
			}
			column, err := columnDefinition(operation.Column)
			if err != nil {
				return "", nil, fmt.Errorf("%w: migration %s add column: %v", ErrSchemaCompilation, item.ID, err)
			}
			fmt.Fprintf(&sql, "ALTER TABLE %s.%s ADD COLUMN %s;\n", quoteIdentifier(targetMember.Model.Namespace), quoteIdentifier(table.ID), column)
		case declaration.MigrationCreateIndex:
			targetMember, table, err := resolveSchemaTable(member, operation.Table, byID)
			if err != nil {
				return "", nil, fmt.Errorf("%w: migration %s create index: %v", ErrSchemaCompilation, item.ID, err)
			}
			if err := validateIndexColumns(table, operation.Index); err != nil {
				return "", nil, fmt.Errorf("%w: migration %s create index: %v", ErrSchemaCompilation, item.ID, err)
			}
			writeIndex(&sql, targetMember, table, operation.Index)
		default:
			return "", nil, fmt.Errorf("%w: migration %s operation %q", ErrSchemaCompilation, item.ID, operation.Kind)
		}
	}
	return sql.String(), predecessors, nil
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
