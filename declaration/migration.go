package declaration

import (
	"fmt"
	"sort"
)

func normalizeTables(tables []Table) {
	sort.Slice(tables, func(i, j int) bool { return tables[i].ID < tables[j].ID })
	for i := range tables {
		table := &tables[i]
		sort.Slice(table.Columns, func(a, b int) bool { return table.Columns[a].ID < table.Columns[b].ID })
		sort.Slice(table.Constraints, func(a, b int) bool { return table.Constraints[a].ID < table.Constraints[b].ID })
		sort.Slice(table.Indexes, func(a, b int) bool { return table.Indexes[a].ID < table.Indexes[b].ID })
	}
}

func validateMigrationSnapshot(migration Migration, imports map[string]struct{}) error {
	if len(migration.Snapshot) == 0 {
		return fmt.Errorf("%w: migration %s requires a schema snapshot", ErrInvalidSchema, migration.ID)
	}
	schemas := map[string]MigrationSchema{}
	tables := map[Ref]Table{}
	namespaces := map[string]bool{}
	for _, schema := range migration.Snapshot {
		if !validNamespace(schema.Namespace) || len(schema.Tables) == 0 {
			return fmt.Errorf("%w: migration %s snapshot schema", ErrInvalidSchema, migration.ID)
		}
		if schema.Member != "" {
			if _, exists := imports[schema.Member]; !exists {
				return fmt.Errorf("%w: migration snapshot import %s", ErrReference, schema.Member)
			}
		}
		if _, exists := schemas[schema.Member]; exists || namespaces[schema.Namespace] {
			return fmt.Errorf("%w: migration snapshot schema %s", ErrDuplicate, schema.Namespace)
		}
		schemas[schema.Member] = schema
		namespaces[schema.Namespace] = true
		for _, table := range schema.Tables {
			if len(table.Columns) == 0 {
				return fmt.Errorf("%w: migration snapshot table %s has no columns", ErrInvalidSchema, table.ID)
			}
			if err := validateTable(table); err != nil {
				return err
			}
			key := Ref{Member: schema.Member, Symbol: table.ID}
			if _, exists := tables[key]; exists {
				return fmt.Errorf("%w: migration snapshot table %s", ErrDuplicate, table.ID)
			}
			tables[key] = table
		}
	}
	for ref, table := range tables {
		for _, constraint := range table.Constraints {
			if constraint.Kind != ConstraintForeignKey {
				continue
			}
			target := constraint.ReferencedTable
			if target.Member == "" {
				target.Member = ref.Member
			}
			referenced, exists := tables[target]
			if !exists || len(constraint.Columns) != len(constraint.ReferencedCols) {
				return fmt.Errorf("%w: migration snapshot foreign key %s", ErrReference, constraint.ID)
			}
			for i, column := range constraint.ReferencedCols {
				left, _ := tableColumn(table, constraint.Columns[i])
				right, ok := tableColumn(referenced, column)
				if !ok || left.Type != right.Type {
					return fmt.Errorf("%w: migration snapshot foreign key column %s", ErrReference, column)
				}
			}
		}
	}
	for _, operation := range migration.Operations {
		if err := validateRef(operation.Table, imports); err != nil {
			return err
		}
		table, exists := tables[operation.Table]
		if !exists {
			return fmt.Errorf("%w: migration snapshot table %s", ErrReference, operation.Table.Symbol)
		}
		emptyIndex := operation.Index.ID == "" && len(operation.Index.Columns) == 0 && !operation.Index.Unique
		switch operation.Kind {
		case MigrationCreateTable:
			if operation.Column != (Column{}) || !emptyIndex {
				return fmt.Errorf("%w: create-table fields", ErrInvalidSchema)
			}
		case MigrationAddColumn:
			if !emptyIndex {
				return fmt.Errorf("%w: add-column fields", ErrInvalidSchema)
			}
			if _, exists := tableColumn(table, operation.Column.ID); exists {
				return fmt.Errorf("%w: add-column %s already in snapshot", ErrDuplicate, operation.Column.ID)
			}
			if err := validateTable(Table{ID: table.ID, Columns: []Column{operation.Column}}); err != nil {
				return err
			}
		case MigrationCreateIndex:
			if operation.Column != (Column{}) {
				return fmt.Errorf("%w: create-index fields", ErrInvalidSchema)
			}
			if err := validateTable(Table{ID: table.ID, Columns: table.Columns, Indexes: []Index{operation.Index}}); err != nil {
				return err
			}
		default:
			return fmt.Errorf("%w: migration operation %s", ErrUnsupported, operation.Kind)
		}
	}
	return nil
}

func tableColumn(table Table, id string) (Column, bool) {
	for _, column := range table.Columns {
		if column.ID == id {
			return column, true
		}
	}
	return Column{}, false
}
