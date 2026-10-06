package compiler

import (
	"crypto/sha256"
	"encoding/hex"

	"github.com/plystra/data/migration"
)

// Artifact pairs one immutable migration definition with its PostgreSQL SQL.
type Artifact struct {
	Definition migration.Definition
	SQL        string
}

// DigestSQL binds exact SQL bytes to their immutable artifact definition.
func DigestSQL(sql string) string {
	sum := sha256.Sum256([]byte(sql))
	return "sha256:" + hex.EncodeToString(sum[:])
}
