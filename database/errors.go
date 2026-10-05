package database

import "errors"

var (
	// ErrInvalidPlan means that a plan cannot be executed safely.
	ErrInvalidPlan = errors.New("invalid database plan")
	// ErrUnavailable means that the selected database Resource is not ready.
	ErrUnavailable = errors.New("database unavailable")
	// ErrConflict is a normalized uniqueness or exclusion conflict.
	ErrConflict = errors.New("database conflict")
	// ErrForeignKey is a normalized foreign-key violation.
	ErrForeignKey = errors.New("database foreign-key violation")
	// ErrConcurrent is a normalized serialization or deadlock failure.
	ErrConcurrent = errors.New("database concurrent mutation")
	// ErrTimeout means the database operation exceeded its context deadline.
	ErrTimeout = errors.New("database timeout")
	// ErrResultUnknown means commit acknowledgement was not determined.
	ErrResultUnknown = errors.New("database result unknown")
	// ErrBackend is a safe catch-all for backend failures.
	ErrBackend = errors.New("database backend failure")
)
