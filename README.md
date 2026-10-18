# Plystra Data

Plystra Data is an independent repository for Data-specific product behavior.

This repository owns Data domain models, persistence semantics, migrations,
Resource providers, and Data application code. The Core repositories remain
generic:

- `github.com/plystra/kernel` owns generic runtime, lifecycle, configuration,
  and public host contracts.
- `github.com/plystra/cli` owns Project discovery, validation, generation, and
  developer commands.
- `github.com/plystra/data` consumes those public contracts; Core does not
  import this repository or contain Data product code.

The repository contains the first independently verifiable Data verticals:
the shared PostgreSQL Resource and the immutable migration planner. Backend
providers own execution; the planner validates migration identity, predecessor
ordering, applied-history drift, and a canonical plan digest without opening a
database connection. PostgreSQL also exposes explicit `MigrationPlan` and
`ApplyMigrations` operations. Apply requires the exact plan digest, takes a
target-scoped transaction lock, records append-only history, and never retries
an indeterminate commit implicitly.

The `declaration` package provides the first compiler-facing logical model:
typed `Member[Access]` roots, tables, columns, constraints, indexes, queries,
atomic plans, migrations, and explicit references. `Normalize` validates the
closed model and returns a stable sorted copy; `Digest` binds that normalized
logical model without including the access Resource value or runtime secrets.
The `postgres/compiler.CompileSchema` lowerer turns normalized models assigned to one
database Resource into deterministic PostgreSQL schema DDL, including scalar
types, defaults, constraints, indexes, and same-Resource foreign keys; it
rejects cross-Resource foreign keys before any backend operation.
`postgres/compiler.CompileQueries` emits deterministic SQL for `SELECT`, `INSERT`,
`UPDATE`, and `DELETE` intent. `INSERT` may target a named primary-key or unique
table constraint on a local or explicitly imported same-Resource table. Its
closed conflict action is `do-nothing` or `do-update`. `do-update` requires a
nonempty explicit `Conflict.UpdateColumns` list; each named column must exist on
the target table and have an explicit insert assignment. The compiler emits
`ON CONFLICT ON CONSTRAINT ... DO UPDATE SET "column" = EXCLUDED."column"` only
for those columns. Cross-Resource and index targets remain unsupported.
Every explicit `SELECT` projection requires one typed `Results` mapping per
output. A `SELECT` without an explicit projection retains its existing `*`
output and has no typed result mapping. Query predicates support comparisons,
`IS NULL`, `IS NOT NULL`, `AND`, and `OR`. Each kind has a closed field shape,
and an absent predicate cannot carry operands or nested items. Invalid shapes
are rejected before SQL emission.
Comparison operands use explicit `ColumnRef` values and accept either an
introduced column or a declared typed parameter/literal. Assignments accept
only declared typed parameters or closed literal values; analysis checks their
columns and value types against local or imported target tables. Projections,
ordering, limit, offset, and cross-Resource references are validated before
lowering. Analysis checks scalar types across local and imported predicate
columns, literals, and named parameters before backend selection; PostgreSQL
lowering repeats the check before emitting SQL.
`SELECT` queries can declare ordered `INNER` and `LEFT` equality joins through
`Query.Joins`. Each join names one table and compares a column on an already
introduced table with a column on the new table using explicit `ColumnRef`
values. Joined columns must have matching scalar types, and all referenced
members must use the same selected database Resource. SELECT projections and
ordering may explicitly name the base table or an already introduced join
table; the SQL lowerer uses stable aliases and qualifies those columns
deterministically. Predicates may compare columns from the base or an already
introduced join table, or compare an introduced column with a typed value;
the closed `COUNT` aggregate may project `*` or an introduced column with an
explicit alias and one required non-null `int64` result mapping. The closed
`MIN` and `MAX` aggregates may each project one introduced `int64` column with
an explicit alias and one required nullable `int64` result mapping. These
inputs are checked during analysis even when the column belongs to an imported
member; PostgreSQL lowering repeats the type check before emission. The
ungrouped forms are single-output SELECTs with no ordinary projection,
aggregate ordering, limit, or offset. A bounded grouped SELECT may project one
or more columns from the base or introduced `INNER`/`LEFT` join tables, named
by matching `GroupBy` columns, plus one `COUNT`, `MIN`, or `MAX` output. It
emits deterministic ordered `GROUP BY` columns and matching typed result
mappings, with no limit or offset. Explicitly imported join tables must use
the same selected database Resource. A grouping output from the nullable side
of a `LEFT JOIN` remains nullable even when `HAVING IS NOT NULL` filters rows.
Grouped ordering names only projected output names without table qualifiers;
PostgreSQL emits an alias-qualified grouping column for joined queries, a
base-table-qualified column without joins, or an aggregate expression. A grouped
query may filter its projected grouping columns or aggregate alias through
`Having`, using the closed comparison, `IS NULL`,
`IS NOT NULL`, `AND`, and `OR` predicate forms. HAVING references are
unqualified output names; the lowerer substitutes the validated column or
aggregate expression rather than relying on PostgreSQL SELECT aliases in the
HAVING clause. Literal and named parameter operands must match the referenced
output's scalar type; analysis checks grouped output comparisons before backend
selection, and PostgreSQL lowering repeats the check. HAVING on an ungrouped
query or mutation is rejected.
`COUNT(column)` ignores NULL values, including unmatched LEFT JOIN rows;
`COUNT(*)` counts joined rows. `MIN` and `MAX` are nullable because an empty
input (or no non-null values) produces SQL NULL. Other aggregate functions
remain unsupported. `COUNT(*)` cannot name a table; count an
introduced column to exclude unmatched LEFT JOIN rows. Queries without joins
retain their existing SQL bytes.
`postgres/compiler.CompileMigrations` lowers immutable migration operations into
planner-validated SQL artifacts with logical and compiled digests, predecessor
identity, compiler identity, and PostgreSQL backend identity.
`postgres/compiler.CompilePlans` currently lowers the initial executable-plan
subset: local, parameter-free `SELECT` statements with explicit cardinality
and typed projected results become ordered `database.Plan` statements. The
database contract carries result width, scalar kind, and nullability into the
PostgreSQL transaction, which validates those facts before commit. Joins,
parameters, imported queries, mutation plans, generated access methods, and
Data-owned artifact installation remain separate integration boundaries.
Mutation queries may additionally declare explicit `Returning` projections and
typed `Results` mappings. Data analysis validates returned columns and result
scalar types and nullability, including imported tables. The lowerer emits
deterministic PostgreSQL `RETURNING` clauses and rejects result sources whose
scalar type or nullability does not match the selected table column. Result
mappings are compiler metadata; typed access provider and codec generation
remains a later integration boundary.
Staged query artifacts and their manifest entries include the declaring query
operation, group-by, optional HAVING, and projection aggregate descriptors,
parameters in PostgreSQL positional order, and typed result metadata.
The `analyzer` package statically discovers directive-bound declaration roots
from a caller-supplied, module-relative source snapshot. Every package has a
required closed `root_eligibility` value: `eligible` packages may contribute
roots, while `support` packages provide declarations and imports for type
checking but cannot contribute roots, even if they contain Data directives.
The marker is part of the snapshot and analyze input digests. The analyzer
type-checks the snapshot without executing package initialization, accepts
only the closed literal grammar, records deterministic source provenance,
validates discovered Resource or `NoAccess` types, decodes and normalizes the
complete member model, validates imported member targets, reference kinds, and
acyclic member imports, and checks explicit `SELECT` result scalar types and
nullability against local and imported projections, including `LEFT JOIN`
outputs. It returns bounded structured diagnostics. It does not
read the host filesystem, import the Core CLI, or write generated
artifacts. The packages are Data-owned and have no dependency on Core CLI
implementation packages.

The pure `postgres/compiler` package owns SQL lowering and does not import the
runtime `postgres` provider. The public compiler command's build dependency
closure excludes Kernel and CLI. It installs both from the checkout with
`GOWORK=off`, `GOPROXY=off`, and an empty module cache, and from a versioned
module archive through a file proxy with no source checkout and an empty module
cache. The runtime `postgres.New` provider remains a separate consumer of
Kernel's Secret contract; these are local publication-consumption checks, not
remote publication or stable-release evidence.

The `database` package exposes bounded backend errors for not-found,
uniqueness or exclusion conflict, foreign-key violation, concurrent mutation,
unavailability, timeout, resource exhaustion, indeterminate commit outcome,
and internal backend failure. The PostgreSQL provider maps SQLSTATE and context
failures to these categories without exposing SQL text, DSNs, driver messages,
or backend payloads. A commit acknowledgement failure remains
`ErrResultUnknown`; it is never silently converted into a confirmed rollback
or retried by the provider.

The `protocol` package and `cmd/plystra-data-compiler` provide the bounded
`plystra.data-analyze/v1` phase. Requests carry an exact compiler identity,
including the selected Go Module `h1:` checksum and raw distribution-manifest
digest, Go build context, limits, and an input digest binding both the build context
and self-contained source snapshot;
responses contain normalized member models with their roots, provenance,
diagnostics, and output digests.
The command writes no generated files and emits no non-protocol stdout. The
distribution contract is recorded in `plystra-data-compiler.json`.

The command also implements `plystra.data-emit/v1` for the currently supported
PostgreSQL schema, query, and migration SQL. An emit request carries the accepted
analyze-result digest, the frozen application-model digest, and explicit active
member assignments to selected `data.database/v1` Resource instances and
`postgres/v1` backends. The input digest binds these facts to the exact compiler
identity and Go build context. For each selected instance, the compiler returns
in-memory staged `schema/schema.sql`, query SQL, immutable migration SQL, and a
`manifest.json` under its allowed `generated/data/<instance>/` root. Every
artifact includes mode, bytes, SHA-256 digest, owning members, Resource,
provider, backend, compiler, and frozen-model provenance. Each migration SQL
artifact also carries its immutable Definition: member-local identity, version,
predecessors, logical declaration digest, compiled artifact digest, compiler,
and backend. The manifest binds logical member digests and the instance's
artifact inventory, including each migration Definition. The response validator
compares staged migration Definitions with the accepted analyzed member models
and selected compiler, rejects migration metadata on other artifacts, and
checks identity, output digest, paths, ownership, byte digests, and manifest
consistency before any caller installs output. Frames are limited to
16 MiB, with at most 4,096 artifacts, 4 MiB per artifact, and 512 bytes per
artifact path. The compiler does not read source files or write generated files
during emit.

This emit phase does not yet generate typed access providers or codecs, or
install staged files in an application. Data-specific activation, generated
access, transactional installation, and drift checks belong outside the
generic Core CLI and remain separate unfinished integration work.

## Module

The module path is `github.com/plystra/data`.

## Local verification

Use ordinary Go Module resolution with `GOWORK=off` when validating this
repository. Keep Data validation and release state independent from the local
`core-workspace/go.work` convenience workspace.
