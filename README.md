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
The `postgres.CompileSchema` lowerer turns normalized models assigned to one
database Resource into deterministic PostgreSQL schema DDL, including scalar
types, defaults, constraints, indexes, and same-Resource foreign keys; it
rejects cross-Resource foreign keys before any backend operation.
`postgres.CompileQueries` emits deterministic SQL for `SELECT`, `INSERT`,
`UPDATE`, and `DELETE` intent. Query predicate operands use explicit
`ColumnRef` values and accept either an introduced column or a declared typed
parameter/literal; assignments accept only declared typed parameters or closed
literal values. Projections, ordering, limit, offset, and cross-Resource
references are validated before lowering.
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
explicit alias and one required non-null `int64` result mapping. COUNT is a
single-output SELECT with no ordinary projection, aggregate ordering, limit,
or offset. `COUNT(column)` ignores NULL values, including unmatched LEFT JOIN
rows; `COUNT(*)` counts joined rows. Other aggregates and conflict/upsert intent
remain unsupported. Queries without joins retain their existing SQL bytes.
`postgres.CompileMigrations` lowers immutable migration operations into
planner-validated SQL artifacts with logical and compiled digests, predecessor
identity, compiler identity, and PostgreSQL backend identity.
Mutation queries may additionally declare explicit `Returning` projections and
typed `Results` mappings. The lowerer emits deterministic PostgreSQL
`RETURNING` clauses and rejects result sources whose scalar type or nullability
does not match the selected table column. Result mappings are compiler
metadata; typed access provider and codec generation remains a later
integration boundary.
Staged query artifacts and their manifest entries include the declaring query
operation, parameters in PostgreSQL positional order, and typed result
metadata.
The `analyzer` package statically discovers directive-bound declaration roots
from a caller-supplied, module-relative source snapshot. It type-checks the
snapshot without executing package initialization, accepts only the closed
literal grammar, records deterministic source provenance, validates discovered
Resource or `NoAccess` types, decodes and normalizes the complete member model,
validates imported member targets, reference kinds, and acyclic member imports,
and returns bounded structured diagnostics. It
does not read the host filesystem, import the Core CLI, or write generated
artifacts. The packages are Data-owned and have no dependency on Core CLI
implementation packages.

The `protocol` package and `cmd/plystra-data-compiler` provide the bounded
`plystra.data-analyze/v1` phase. Requests carry an exact compiler identity,
Go build context, limits, and an input digest binding both the build context
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
install staged files in Core CLI. CLI compiler acquisition, member
activation, graph projection, transactional installation, and drift checks
remain separate unfinished integration work.

## Module

The module path is `github.com/plystra/data`.

## Local verification

Use ordinary Go Module resolution with `GOWORK=off` when validating this
repository. Keep Data validation and release state independent from the local
`core-workspace/go.work` convenience workspace.
