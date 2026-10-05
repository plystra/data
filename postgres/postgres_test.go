package postgres_test

import (
	"testing"
	"time"

	"github.com/plystra/data/postgres"
)

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	tests := []postgres.Config{
		{},
		{MaxConnections: 0, HealthTimeout: time.Second},
		{MaxConnections: 4, MinConnections: 5, HealthTimeout: time.Second},
	}
	for _, config := range tests {
		if value, err := postgres.New(config); value != nil || err == nil {
			t.Fatalf("New(%#v) = (%v, %v), want invalid configuration", config, value, err)
		}
	}
}
