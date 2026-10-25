package postgres_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/plystra/data/database"
	"github.com/plystra/data/postgres"
	"github.com/plystra/kernel/configuration"
	"github.com/plystra/kernel/invocation"
)

func TestExecPublicNormalizedOutcomes(t *testing.T) {
	for _, test := range []struct {
		name, code string
		dropCommit bool
		want       error
	}{
		{name: "not found", want: database.ErrNotFound},
		{name: "conflict", code: "23505", want: database.ErrConflict},
		{name: "statement unknown", code: "40003", want: database.ErrResultUnknown},
		{name: "lost commit acknowledgement", dropCommit: true, want: database.ErrResultUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			provider, commits := protocolDatabase(t, test.code, test.dropCommit)
			result, err := provider.Exec(context.Background(), database.Plan{Statements: []database.Statement{{
				ID: "probe", SQL: "SELECT 1", ReturnsRows: true,
				Cardinality: database.CardinalityExactlyOne, ResultColumns: []database.ResultColumn{{Kind: database.ValueInt64}},
			}}})
			if !errors.Is(err, test.want) || len(result.Statements) != 0 {
				t.Fatalf("Exec = %#v, %v; want %v and no published result", result, err, test.want)
			}
			if strings.Contains(err.Error(), "private") {
				t.Fatalf("backend detail leaked: %v", err)
			}
			translated := invocation.NewSemanticError("storage_failed", fmt.Errorf("domain: %w", err))
			if test.want == database.ErrResultUnknown {
				var marker *invocation.ResultUnknownError
				if !errors.As(err, &marker) || translated.Completion() != invocation.CompletionResultUnknown || !errors.Is(translated, database.ErrResultUnknown) {
					t.Fatal("uncertainty marker lost at public boundary")
				}
			} else if translated.Completion() != invocation.CompletionResultKnown {
				t.Fatal("known failure became uncertain")
			}
			if test.dropCommit {
				if got := <-commits; got != 1 {
					t.Fatalf("commit attempts = %d, want 1", got)
				}
			}
		})
	}
}

// A small PostgreSQL protocol peer makes commit acknowledgement loss
// deterministic while exercising New, Start and Exec without a private hook.
func protocolDatabase(t *testing.T, code string, dropCommit bool) (*postgres.Database, <-chan int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	commits := make(chan int, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
		peer := pgproto3.NewBackend(conn, conn)
		if _, err := peer.ReceiveStartupMessage(); err != nil {
			return
		}
		peer.Send(&pgproto3.AuthenticationOk{})
		peer.Send(&pgproto3.ParameterStatus{Name: "server_version", Value: "17.0"})
		peer.Send(&pgproto3.ParameterStatus{Name: "client_encoding", Value: "UTF8"})
		peer.Send(&pgproto3.ParameterStatus{Name: "standard_conforming_strings", Value: "on"})
		peer.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
		peer.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
		if peer.Flush() != nil {
			return
		}
		status := byte('I')
		for {
			message, err := peer.Receive()
			if err != nil {
				return
			}
			query, ok := message.(*pgproto3.Query)
			if !ok {
				return
			}
			sql := strings.ToLower(strings.TrimSpace(query.String))
			switch {
			case strings.HasPrefix(sql, "begin"):
				status = 'T'
				peer.Send(&pgproto3.CommandComplete{CommandTag: []byte("BEGIN")})
			case sql == "commit":
				commits <- 1
				if dropCommit {
					return
				}
				status = 'I'
				peer.Send(&pgproto3.CommandComplete{CommandTag: []byte("COMMIT")})
			case sql == "rollback":
				status = 'I'
				peer.Send(&pgproto3.CommandComplete{CommandTag: []byte("ROLLBACK")})
			case strings.HasPrefix(sql, "select"):
				if code != "" {
					status = 'E'
					peer.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: code, Message: "private SQL and credentials"})
				} else {
					peer.Send(&pgproto3.RowDescription{Fields: []pgproto3.FieldDescription{{Name: []byte("value"), DataTypeOID: 20, DataTypeSize: 8, TypeModifier: -1}}})
					if dropCommit {
						peer.Send(&pgproto3.DataRow{Values: [][]byte{[]byte("1")}})
					}
					peer.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
				}
			default:
				peer.Send(&pgproto3.CommandComplete{CommandTag: []byte("SELECT 1")})
			}
			peer.Send(&pgproto3.ReadyForQuery{TxStatus: status})
			if peer.Flush() != nil {
				return
			}
		}
	}()
	t.Cleanup(func() { _ = listener.Close(); <-done })
	t.Setenv("PLYSTRA_PROTOCOL_DATABASE", "postgres://probe@"+listener.Addr().String()+"/probe?sslmode=disable&default_query_exec_mode=simple_protocol")
	resolver, err := configuration.NewResolver(configuration.ResolverOptions{MaximumValueBytes: 4096})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := configuration.NewEnvironmentReference("PLYSTRA_PROTOCOL_DATABASE")
	if err != nil {
		t.Fatal(err)
	}
	secret, err := resolver.Resolve(context.Background(), reference)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := postgres.New(postgres.Config{URL: secret, MaxConnections: 1, HealthTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Stop(context.Background()) })
	if err := provider.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	return provider, commits
}
