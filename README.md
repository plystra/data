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

## Module

The module path is `github.com/plystra/data`.

## Local verification

Use ordinary Go Module resolution with `GOWORK=off` when validating this
repository. Keep Data validation and release state independent from the local
`core-workspace/go.work` convenience workspace.
