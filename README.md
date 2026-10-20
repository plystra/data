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
Queries and plans may name an explicit exported Go access `Method`. The
normalized model and digest preserve that mapping, and duplicate or invalid
method names fail declaration analysis. The `Method` field alone supplies
identity: Data requires exact name coverage against a type-checked authored
access Resource before accepting its analysis result, including embedded
methods; unmatched, unexported, and lifecycle methods are rejected. A directly
callable SELECT query additionally declares `Cardinality` (`exactly_one`,
`zero_or_one`, or `many`) and maps each parameter and typed result through an
exported `GoField`.
Its authored method has the shape
`Method(context.Context, Request) (Row, error)`, `(*Row, error)`, or
`([]Row, error)` respectively. `Request` and `Row` are exported named structs
whose fields exactly match the explicit mappings; nullable result fields are
`*T`. A directly callable `INSERT`, `UPDATE`, or `DELETE` with explicit
`RETURNING` projections and complete typed result mappings uses the same
cardinality and typed request/row shapes. Its cardinality constrains returned
rows. Without `RETURNING`, a directly callable mutation declares
`Cardinality: none` and has the exact method shape
`Method(context.Context, Request) (uint64, error)`; the `uint64` is the
affected-row count, and there is no typed row mapping. The initial validated
access scalar subset is `bool`, `[]byte`, `float64`, `int64`, and `string`.
Every parameter of a directly callable query must be used by an assignment,
predicate, or grouped `HAVING` expression; unused request fields fail analysis.
Unsupported signatures and callable plans fail analysis. Accepted method types
and cardinality are bound to the analyze result, but provider generation and
installation are not yet present.
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
declared typed parameters or closed literal values. An `UPDATE` assignment
may instead increment a non-null `int64` target column by one, with no value
operand; `INSERT` cannot use this form. Analysis checks assignment columns
and value or increment types against local or imported target tables.
Projections, ordering, limit, offset, and cross-Resource references are
validated before lowering. Analysis checks scalar types across local and
imported predicate columns, literals, and named parameters before backend
selection; PostgreSQL lowering repeats the check before emitting SQL.
`SELECT` queries can declare ordered `INNER` and `LEFT` equality joins through
`Query.Joins`. Each join names one table and compares a column on an already
introduced table with a column on the new table using explicit `ColumnRef`
values. Joined columns must have matching scalar types, and all referenced
members must use the same selected database Resource. SELECT projections and
ordering may explicitly name the base table or an already introduced join
table; the SQL lowerer uses stable aliases and qualifies those columns
deterministically. Predicates may compare columns from the base or an already
introduced join table, or compare an introduced column with a typed value;
each closed `COUNT` output may project `*` or an introduced column with an
explicit alias and one required non-null `int64` result mapping. Each closed
`MIN` or `MAX` output projects one introduced `int64` column with an explicit
alias and one required nullable `int64` result mapping. These
inputs are checked during analysis even when the column belongs to an imported
member; PostgreSQL lowering repeats the type check before emission. The
closed `SUM` output accepts one introduced `float64` column with an explicit
alias and required nullable `float64` result mapping; integer `SUM` is not
supported because PostgreSQL widens `bigint` sums to `numeric`. Its input is
checked during analysis and again during PostgreSQL lowering. The ungrouped
forms may combine one or more aggregate outputs with no ordinary
projection, aggregate ordering, limit, or offset. A bounded grouped SELECT
may project one or more columns from the base or introduced `INNER`/`LEFT`
join tables, named by matching `GroupBy` columns, plus one or more `COUNT`,
`MIN`, `MAX`, or `SUM` outputs. It emits deterministic ordered `GROUP BY` columns
and matching typed result mappings. Explicitly imported join tables must use
the same selected database Resource. A grouping output from the nullable side
of a `LEFT JOIN` remains nullable even when `HAVING IS NOT NULL` filters rows.
Grouped ordering names only projected output names without table qualifiers;
PostgreSQL emits an alias-qualified grouping column for joined queries, a
base-table-qualified column without joins, or an aggregate expression. Grouped
queries may use literal `LIMIT` and `OFFSET` when ordering includes every
projected grouping key; an aggregate output may precede those keys. Explicit
zero bounds are allowed. Values without their presence flag or beyond
PostgreSQL's signed 64-bit range are rejected before SQL emission. An ungrouped
aggregate query still cannot use ordering, limit, or offset. A grouped query may
filter its projected grouping columns or any aggregate output through
`Having`, using the closed comparison, `IS NULL`,
`IS NOT NULL`, `AND`, and `OR` predicate forms. HAVING references are
unqualified output names; the lowerer substitutes the validated column or
aggregate expression rather than relying on PostgreSQL SELECT aliases in the
HAVING clause. Literal and named parameter operands must match the referenced
output's scalar type; analysis checks grouped output comparisons before backend
selection, and PostgreSQL lowering repeats the check. HAVING on an ungrouped
query or mutation is rejected.
`COUNT(column)` ignores NULL values, including unmatched LEFT JOIN rows;
`COUNT(*)` counts joined rows. `MIN`, `MAX`, and `SUM` are nullable because an
empty input (or no non-null values) produces SQL NULL. Other aggregate functions
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
and a private source-snapshot manifest. The manifest names one private root
and carries package eligibility, module-relative file paths, byte counts,
content digests, selected standard-library export identities, Resource
contracts, and analysis limits; source and export bytes stay out
of the protocol frame. `protocol.WriteSnapshot` stages a validated in-memory
inventory into a new private root, and the compiler rechecks every listed file
before analysis. Symlinks, changed bytes, unsafe paths, and out-of-bounds
snapshots are rejected. The published bounds allow at most 4,096 source files,
64 MiB total source bytes, 4 MiB per source file, and 512 bytes per source path.
Standard-library `gc` export inputs have separate bounds of 1,024 packages,
128 MiB total, and 16 MiB each. The analyzer resolves standard imports only
from verified staged exports, handles `unsafe` intrinsically, applies target
architecture sizes, and rejects reached cgo packages without a supported type
input. It never uses ambient package type lookup.
Responses contain normalized member models with their roots, provenance,
diagnostics, and output digests. `project.StageAnalyzeInput` obtains the
Go-selected source closure and standard-library `gc` exports for the selected
Project toolchain, target build context, and tags, then verifies their private
snapshot without compiling authored packages. It discovers Resource directive
identities only from selected Plystra Project modules in that source closure
and includes them in the snapshot digest; the analyzer checks the referenced
Go access type. This does not yet establish complete Core Resource contract
compatibility or generate an access provider.
`project.AnalyzeCurrentProject` builds the verified versioned compiler from the
Project module graph with
`GOWORK=off`, sends one bounded analyze frame, validates the response, and
rechecks compiler selection and source inputs before returning. It does not
compile authored packages or install Project artifacts. Authored access
Resource members can now be analyzed, but Data-owned provider generation and
installation remain incomplete.

The compiler command writes no generated files and emits no non-protocol
stdout. The distribution contract is recorded in
`plystra-data-compiler.json`.

The command also implements `plystra.data-emit/v1` for the currently supported
PostgreSQL schema, query, and migration SQL. An emit request carries the accepted
analyze-result digest, the frozen application-model digest, and explicit active
member assignments to selected `data.database/v1` Resource instances and
`postgres/v1` backends. The input digest binds these facts to the exact compiler
identity and Go build context. For each selected instance, the compiler returns
in-memory staged `schema/schema.sql`, query SQL, immutable migration SQL, and a
`manifest.json` under its allowed `dataaccess/<instance>/` root. Every
artifact includes mode, bytes, SHA-256 digest, owning members, Resource,
provider, backend, compiler, and frozen-model provenance. Each migration SQL
artifact also carries its immutable Definition: member-local identity, version,
predecessors, logical declaration digest, compiled artifact digest, compiler,
and backend. The manifest binds logical member digests and the instance's
artifact inventory, including each migration Definition. The response validator
compares staged migration Definitions with the accepted analyzed member models
and selected compiler, independently recompiles expected schema and query SQL
digests from those models and active Resource assignments, requires migration
SQL bytes to match their accepted compiled artifact digests, rejects migration
metadata on other artifacts, and checks identity, output digest, paths,
ownership, byte digests, and manifest consistency before any caller installs
output. Frames are limited to
16 MiB, with at most 4,096 artifacts, 4 MiB per artifact, and 512 bytes per
artifact path. The compiler does not read source files or write generated files
during emit.

This emit phase does not yet generate typed access providers or codecs, or
install staged files in an application. Data-specific activation, generated
access, transactional installation, and drift checks belong outside the
generic Core CLI and remain separate unfinished integration work.

The independent `plystra-data inspect compiler --project DIR` command selects
the exact `github.com/plystra/data` version from the Project's Go module graph
with `GOWORK=off`. It rejects Data module replacements, verifies the selected
module source against the full `h1:` checksum recorded in the Project's
`go.sum`, validates the distribution manifest, and reports the raw manifest
digest and Project-selected Go toolchain. `--format json`
emits `plystra.data-compiler-inspection/v1`; `--offline` requires a warm module
cache and never uses the network. A Project without the selected version's
full-module `go.sum` entry must first acquire that checksum through Go's module
workflow. Inspection does not invoke the compiler, analyze declarations,
generate providers, or modify Project files.

The Data-owned `project.ParseAssignments` parser accepts the closed
`plystra.data.yaml` `data.members` mapping with exact versioned member IDs,
database Resource instance names, and optional access Resource instance names.
`plystra-data inspect assignments --project DIR` reads that document without
changing Project files and lists assignments in member-ID order. Use
`--format json` for a `plystra.data-assignments/v1` result. Duplicate keys,
unknown fields, invalid names, duplicate access names, and access names that
collide with an assigned database Resource are rejected. Assignment inspection
does not analyze declarations, select a compiler, activate members, generate
providers, or install artifacts; those Data-owned Project workflows remain
incomplete.

`plystra-data inspect sources --project DIR` reads the selected Go module graph
and reports Go-selected authored files in packages with active `//plystra:data`
directives across the current and dependency Plystra Projects. It excludes
generated output, `dataaccess/`, vendor, test, fixtures, hidden, and
nested-module trees;
the selected package files follow ordinary build constraints without requiring
a successful build or a prior generated provider. Applicable `go.work`
selection and `GOFLAGS` build tags are honored; `--tags` overrides the latter.
Go source overlays, alternate module files, and `-toolexec` supplied through
`GOFLAGS` are rejected because they would bypass the recorded source snapshot.
The command checks every versioned Project module source against a complete
`h1:` entry in Project `go.sum` or applicable `go.work.sum` before inspecting
it. Local replacements and workspace modules retain their source origin.
Text and `plystra.data-source-inventory/v1` JSON output contain module version,
replacement and checksum provenance, selected paths, sizes, digests, and Go
build context, never source bytes. `--offline` requires the relevant module
sources to be cached. The scan also rejects authored packages that import
`dataaccess/` even when they contain no Data directive. Default inspection
reports declaration packages only; it does not validate authored access
Resource contracts, invoke the selected compiler, or install Data output.

`inspect sources --closure` additionally follows the Go-selected imports of
each Data root package and reports non-standard dependencies as `support`
packages. Those packages cannot contribute Data roots; selected source bytes
and versioned module identities are checked under the combined source-snapshot
bounds. The output is a read-only source inventory, not yet a compiler-ready
analyze request: selected-toolchain standard-library type data, cgo input, and
compiler invocation are not supplied by this command.

`plystra-data inspect inputs --project DIR` obtains the selected source closure
and its standard-library exports, stages and verifies a private snapshot, and
removes that temporary snapshot after inspection. `--tags` and `--offline`
follow source inspection. Text reports selected input counts;
`plystra.data-input-inspection/v1` JSON also lists standard export and selected
Resource contract identities and the build context. Both include the Go
toolchain, source-package count, and root-independent snapshot digest without
exposing source bytes, export bytes, private paths, or build-cache paths. An authored
package need not build for this type-input acquisition to succeed. This
inspection does not select or invoke the exact Data compiler, accept a member
model, or install generated access.

`plystra-data analyze --project DIR` selects the exact Project-pinned Data
compiler, stages selected source and standard type inputs, and returns a
validated analysis result. `--format json` emits
`plystra.data-analysis/v1`; `--tags` and `--offline` follow input inspection.
The selected Data module's full checksum must be present in Project `go.sum`.
The command leaves Project files unchanged and does not emit or install
generated access, activate members, or alter Core selection.

`plystra-data check assignments --project DIR` reads the closed
`plystra.data.yaml` document and invokes that same selected analyzer before
validating explicit member activation. It requires each assigned member to
have an accepted root, enforces access-instance and per-database namespace
ownership, and requires every import of an active member to have its own
assignment to the same database Resource. Visible unassigned roots remain
dormant. `--format json` emits `plystra.data-activation-check/v1` with sorted
active members and their logical-model digests; `--tags` and `--offline` have
the same meaning as for `analyze`. The check is read-only and does not verify
that named database instances exist, select a backend, generate a Resource
provider, or install Data artifacts.
The `project.ValidateDatabaseSelections` API accepts the Core CLI's generic
configured-Resource facts after active-member validation. It requires each
assigned database instance to have a visible `data.database/v1` provider at
`github.com/plystra/data/postgres.New` and selects `postgres/v1`; an invisible
generated access provider remains permitted. This API does not invoke the CLI,
validate the complete application graph, or install files.

`plystra-data check resources --project DIR` runs the selected Data analyzer,
validates explicit member activation, and invokes the installed `plystra`
command's generic configured-Resource inspection. Every assigned database must
select the visible `github.com/plystra/data/postgres.New` provider for
`data.database/v1`; absent generated access providers do not block this check.
The command uses the default Core configuration selection and current Go build
environment, with `--offline` restricting both processes to cached inputs.
`--format json` emits `plystra.data-resource-check/v1` with active members and
name-sorted database selections. It is read-only and does not validate full
access method shapes, generate access, or install output. Install a compatible
Core CLI as `plystra` on `PATH` before using this command.

## Module

The module path is `github.com/plystra/data`.

## Local verification

Use ordinary Go Module resolution with `GOWORK=off` when validating this
repository. Keep Data validation and release state independent from the local
`core-workspace/go.work` convenience workspace.
