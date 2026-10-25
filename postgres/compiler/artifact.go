package compiler

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/plystra/data/migration"
)

// Artifact pairs one immutable migration definition with its PostgreSQL SQL.
type Artifact struct {
	Definition migration.Definition `json:"definition"`
	SQL        string               `json:"sql"`
}

// MigrationHistory supplies retained exact artifacts and the observed applied
// prefix. Every applied identity must have retained evidence; compilation never
// reconstructs a missing applied artifact from source.
type MigrationHistory struct {
	Artifacts []Artifact               `json:"artifacts"`
	Applied   []migration.HistoryEntry `json:"applied"`
}

// DigestSQL binds exact SQL bytes to their immutable artifact definition.
func DigestSQL(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return "sha256:" + hex.EncodeToString(sum[:])
}
