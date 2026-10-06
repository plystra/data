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
Go build context, limits, input digest, and self-contained source snapshot;
responses contain normalized member models with their roots, provenance,
diagnostics, and output digests.
The command writes no generated files and emits no non-protocol stdout. The
distribution contract is recorded in `plystra-data-compiler.json`.

## Module

The module path is `github.com/plystra/data`.

## Local verification

Use ordinary Go Module resolution with `GOWORK=off` when validating this
repository. Keep Data validation and release state independent from the local
`core-workspace/go.work` convenience workspace.
