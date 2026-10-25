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
Fresh history is detected through a read-only catalog check, so planning never
creates schema and applying a fresh plan does not abort its transaction on a
missing history table. The approved digest is rechecked under the target lock
before creating history or running artifacts. Artifact and history writes
commit together; a failed artifact leaves neither partial schema nor history.

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
Callable atomic plans accept 1-64 ordered local SELECT, INSERT, UPDATE, or
DELETE query references. `Plan.Parameters []Parameter` declares typed inputs;
each callable plan parameter maps to a unique exported request `GoField`.
Every query parameter must be used by its query and bound exactly once through
`PlanStatement.Bindings []PlanBinding`. A binding names its target `Parameter`
and supplies either `FromParameter`, or both `FromStatement` and `FromResult`.
All plan parameters must be used, and source and target scalar types must
match exactly. A result binding must name a non-null result from a strictly
earlier `exactly_one` statement. Missing, duplicate, mixed, forward, self,
nullable, and type-mismatched bindings fail normalization. Literal values stay
in query `Value` declarations; imported plan query references are unsupported.
Normalization sorts plan parameters and bindings while preserving statement
and output order; the logical digest binds the complete parameter/binding model.

Each SELECT statement requires explicit row cardinality and no affected-row
assertion; callable SELECTs also require explicit projections and complete
typed results. Every mutation requires an explicit `Affected` interval with
`Set: true`. A mutation with `RETURNING` additionally requires row cardinality;
without `RETURNING`, its cardinality is `none`. Callable plan methods have the
exact signature `Method(context.Context, Request) (Result, error)`, with
exported named non-generic structs. Request fields exactly match plan parameter
`GoField` mappings. `PlanOutput` mappings may select a subset of typed results
across named `exactly_one` statements into unique result fields; nullable
outputs use `*T`. Zero outputs require an empty named result struct. Unsupported
signatures and field mappings fail analysis. Accepted query and plan method
type identities are bound to the analyze result.

`postgres/compiler.CompilePlans` lowers these bindings and assertions into
typed atomic plan templates. PostgreSQL provider generation supports the
directly callable SELECTs and callable atomic plans described below. Directly
callable mutation queries and Project installation remain incomplete.
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
`postgres/compiler.CompileMigrations(members, compiler, history)` lowers
immutable migration operations into planner-validated SQL artifacts with logical
and compiled digests, predecessor identity, compiler identity, and PostgreSQL
backend identity. Every authored migration requires its own `Snapshot` of
`MigrationSchema` values: the namespace and table definitions used by that
migration, including referenced foreign-key tables. Empty `Member` names the
owning member; other members must be explicit imports assigned to the same
database Resource. These definitions are independent of today's `Model.Tables`
and namespace. A later column addition cannot change an earlier CREATE TABLE.
Create-table includes the snapshot table's constraints and indexes; add-column
uses a snapshot of the table before that addition; create-index validates its
columns against that migration's snapshot. Operations execute in authored order.
Snapshot tables, namespaces, imported references, and operation fields are
validated and included in the immutable logical migration digest.

`compiler.MigrationHistory` carries retained exact SQL artifacts and the observed
applied prefix. Use an empty value only for a target with no observed history.
Each applied identity requires a retained artifact; missing, corrupt, duplicate,
deleted, or changed historical evidence blocks compilation. Retained definitions
must match the authored identity, version, predecessors, and logical snapshot
digest, and their SQL must match the original artifact checksum. The compiler
reuses their exact bytes and original compiler identity while compiling new
migrations with the selected compiler. The planner compares each history entry
with that artifact's original compiler, and approval still binds the currently
selected compiler. Callers must read the target's current history and supply
the retained artifacts; filesystem history storage and command orchestration
remain incomplete. The backend re-reads current history under its apply lock.
`postgres/compiler.CompilePlans` lowers ordered local SELECT and mutation
queries into `PlanArtifact` values. Each artifact includes the normalized plan
`Definition` and an unbound runtime `Plan`. Query parameters follow normalized
PostgreSQL placeholder order; bindings become named input references or typed
references to an earlier result's projected column, including `RETURNING`
aliases. Mutations preserve their affected-row intervals independently of row
cardinality. SELECT and RETURNING results retain width, scalar kind and
nullability for validation inside the transaction. The five access scalars are
supported; imported query references and other scalar codecs are rejected.
The artifact digest binds the complete normalized declaration and runtime
template, including request fields, output mappings, bindings and assertions.
Compilation validates templates with temporary typed probes but returns no
invented input values. Callers must bind every request input before execution.
Generated plan methods bind these templates through authored access contracts.
Data-owned artifact installation remains incomplete.

The runtime `database.Plan` can bind an argument to a prior result with
`database.ResultArgument(kind, statementID, zeroBasedColumn)`. Validation
requires an earlier exactly-one row-producing statement, an explicit matching
non-null result-column contract, and no simultaneous literal payload. Forward,
self, missing, nullable, multi-row and type-mismatched references fail before
pool acquisition. PostgreSQL resolves these values only within that one
SERIALIZABLE transaction. A row-producing mutation may also carry its explicit
affected-row assertion; both returned-row cardinality and the backend command
count must pass before commit. Any downstream statement, decode, assertion or
cancellation failure rolls back all prior effects and publishes no result.
This runtime capability does not supply cross-member composition or Project
installation.
Named request values can be bound with `database.InputArgument(kind, inputID)`
and `Plan.Inputs`. Every supplied input must be a closed, non-null scalar of the
expected kind and be referenced; missing, unused, nested, mixed-source or excess
inputs fail before pool acquisition. Inputs remain driver parameters and may
be reused across statements. A parameterized template with no bound inputs is
not executable. Compiled templates must never contain actual request secrets.
Mutation queries may additionally declare explicit `Returning` projections and
typed `Results` mappings. Data analysis validates returned columns and result
scalar types and nullability, including imported tables. The lowerer emits
deterministic PostgreSQL `RETURNING` clauses and rejects result sources whose
scalar type or nullability does not match the selected table column. Result
mappings are compiler metadata; mutation access providers and their codecs
remain a later integration boundary.
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
outputs. For callable plans, it decodes parameters and statement bindings,
validates binding sources and assertions, and matches request fields and
outputs against the authored method's named structs. Output types are resolved
by both statement and result identity, so different statements may reuse a
result name. It returns bounded structured diagnostics. It does not
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
identifiable through `errors.Is(err, database.ErrResultUnknown)` and carries
Kernel's `invocation.NewResultUnknown` runtime marker, including through
`invocation.NewSemanticError`. It is never silently converted into a confirmed
rollback or retried by the provider. An exactly-one lookup with no rows
preserves `ErrNotFound`; a server-confirmed rollback is a known failure.

`database.Plan.Validate` accepts only the closed query cardinalities and
argument kinds (including explicit `ValueNull`). PostgreSQL validates these
inputs before acquiring a connection or beginning a transaction; unknown or
empty argument kinds cannot silently become SQL NULL.

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
Resource members can now be analyzed. The separate emit phase stages supported
SELECT and atomic plan providers. `project.PlanCurrentProject` orchestrates read-only
analysis and emission; transactional installation remains incomplete.

The compiler command writes no generated files and emits no non-protocol
stdout. The distribution contract is recorded in
`plystra-data-compiler.json`.

The command also implements `plystra.data-emit/v1` for the currently supported
PostgreSQL schema, query, and migration SQL and bounded typed SELECT access
providers. An emit request carries the accepted
analyze-result digest, the frozen Data generation-input digest, and explicit active
member assignments to selected `data.database/v1` Resource instances and
`postgres/v1` backends. Optional `migration_history` entries bind retained
artifacts and observed history to one selected Resource. Their artifact
inventory is canonicalized while applied-history order is preserved; both are
bound into the input digest. The input digest binds these facts to the exact
compiler identity and Go build context. For each selected instance, the compiler returns
in-memory staged `schema/schema.sql`, query SQL, immutable migration SQL,
`providers/<member-id>/provider.go` for each supported access Resource, and a
`manifest.json` under its allowed `dataaccess/<instance>/` root. Every
artifact includes mode, bytes, SHA-256 digest, owning members, Resource,
provider, backend, compiler, and frozen-model provenance. Each migration SQL
artifact also carries its immutable Definition: member-local identity, version,
predecessors, logical declaration digest, compiled artifact digest, compiler,
and backend. The manifest binds logical member digests and the instance's
artifact inventory, including each migration Definition. The response validator
compares staged migration Definitions with the accepted analyzed member models
and verified retained history, preserving historical SQL and compiler identity
rather than recompiling it. The staging envelope records the current emit
compiler separately from the migration Definition's original compiler. The
validator independently recompiles expected schema and query SQL
digests and generated provider source from those models, accepted access type
identities, and active Resource assignments, requires migration
SQL bytes to match their accepted compiled artifact digests, rejects migration
metadata on other artifacts, and checks identity, output digest, paths,
ownership, byte digests, and manifest consistency before any caller installs
output. Frames are limited to
16 MiB, with at most 4,096 artifacts, 4 MiB per artifact, and 512 bytes per
artifact path. The compiler does not read source files or write generated files
during emit.

`postgres/compiler.CompileAccessProviders(members, acceptedRoots)` is the pure
Go provider generator used by emit. It returns deterministic source, path,
owning member, and byte digest. Every provider includes a compile-time
assignment to its authored access Resource and an ordinary directive-bound
`New(database database.Resource) (*Provider, error)` constructor. It borrows
the existing Resource without acquiring a pool or owning lifecycle hooks.
Generated query methods submit single-statement `database.Plan` values with literal
compiled SQL, parameters in normalized PostgreSQL placeholder order, complete
result-column contracts, and explicit cardinality. They preserve backend
errors and decode fields by projection source and explicit `GoField`, checking
statement count, row width, scalar kinds, nullability, and cardinality before
publishing typed results. Byte parameters and results are copied; zero-length
`ValueBytes` is non-null bytea, while SQL NULL requires `ValueNull`.

The supported generated methods are parameterized or parameter-free SELECTs
with `exactly_one`, `zero_or_one`, or `many` cardinality and the five validated
access scalars (`bool`, `[]byte`, `float64`, `int64`, `string`), including `*T`
nullable results. Callable plan methods submit every ordered SELECT/mutation
statement in one `Exec`, binding copied request inputs and earlier results.
They preserve all cardinality, typed-column and independent affected-row
assertions, decode named outputs across statements (including nullable and
byte results), and support named empty result structs for no-output plans.
Backend failures, including indeterminate commits, propagate without partial
typed output or retries. Provider-side validation rejects malformed result
inventories; the PostgreSQL Resource enforces result/assertion contracts before
commit. Named generated-public-contract tests run normal/race, and disposable
PostgreSQL tests exercise generated plan commit and later-conflict rollback.
A Resource containing an unsupported directly callable mutation query
or incomplete accepted method metadata rejects the whole emit result;
generation never silently omits a method. Emit validates generated source
against accepted intent even when a changed artifact, manifest, and response
are rehashed consistently.

Access contracts and their named request/row types must use public import
paths. This portable-provider subset rejects an exact `internal` or `vendor`
path segment before successful emit, even when that package is importable by
the authored member. Emit has no consumer package identity and cannot assume
that downstream `dataaccess/` output has the same visibility. Move these
public types to an ordinary public package; similarly spelled public segments
such as `internalized` remain supported.

Emit still does not install staged files in an application. Data-specific
Project installation and drift checks belong outside the generic Core CLI and
remain separate unfinished work. Read-only planning is described below. This
bounded provider subset does not complete AuthN/AuthZ persistence or their
cross-member atomic integration plans.

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
providers, or install artifacts; use the separate Data-owned analysis and
planning commands for validated in-memory output. Installation remains incomplete.

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

`plystra-data plan --project DIR [--format text|json] [--offline]` produces a
validated in-memory Data artifact plan, including when `dataaccess/` is absent.
It reads the same default Core configured-Resource facts, exact Project-selected
compiler, authored declarations, and `plystra.data.yaml` assignments. It builds
the verified compiler once and invokes its analyze and emit phases; it never
uses a compiler from `PATH`, compiles authored packages, runs application tests,
or executes authored initialization. `--offline` requires cached module and
toolchain inputs for both Data and Core inspection. A compatible `plystra` must
be on `PATH`.

`project.PrepareEmitRequest` validates accepted analysis, active-member closure,
and PostgreSQL database selections before building the emit request. The frozen
generation-input digest binds the Project module, source/type snapshot, analyze
result, exact compiler, Go build context, active access-instance assignments,
and configured Resource provenance. Unassigned declarations remain dormant.
`project.PlanCurrentProject` validates the complete compiler response and
rechecks compiler, source/type, module, workspace, and assignment inputs. The
command also rechecks Core configured-Resource facts before returning output;
changed inputs or rejected analysis/emission return no artifact plan.

JSON output has schema `plystra.data-emit-plan/v1` with the validated `request`
and `response`, including artifact bytes and provenance; text lists artifact
paths and digests. Planning changes no Project files, selects or binds no Core
Resource, and installs nothing. It does not inspect a database, observe target
migration history, supply retained historical artifacts, or approve/apply a
migration. It is a generation artifact plan, separate from migration planning;
retained-history orchestration, transactional installation, and drift checks
remain incomplete.

## Module

The module path is `github.com/plystra/data`.

## Local verification

Use ordinary Go Module resolution with `GOWORK=off` when validating this
repository. Keep Data validation and release state independent from the local
`core-workspace/go.work` convenience workspace.
