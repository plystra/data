package database

import "context"

// Resource is the canonical Data database contract. It is process-local
// infrastructure and is never projected as an external Interface.
//
//plystra:resource data.database/v1
type Resource interface {
	Exec(context.Context, Plan) (Result, error)
	Health(context.Context) error
}
