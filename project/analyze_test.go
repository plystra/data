package project

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/plystra/data/protocol"
)

func TestMain(m *testing.M) {
	mode := os.Getenv("PLYSTRA_DATA_TEST_COMPILER_CHILD")
	if mode == "" {
		os.Exit(m.Run())
	}
	if mode == "malformed" {
		_, _ = os.Stdout.Write([]byte("not a frame"))
		os.Exit(0)
	}
	if mode == "nonzero" {
		os.Exit(7)
	}
	if mode == "stderr" {
		_, _ = os.Stderr.Write([]byte("unexpected diagnostic"))
	}
	if err := protocol.WriteFrame(os.Stdout, protocol.AnalyzeResponse{Schema: protocol.AnalyzeSchema}); err != nil {
		os.Exit(8)
	}
	if mode == "trailing" {
		_, _ = os.Stdout.Write([]byte("extra"))
	}
	os.Exit(0)
}

func TestInvokeSelectedCompilerRejectsInvalidProcessOutput(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(binary) {
		t.Fatalf("test executable is not absolute: %q", binary)
	}
	for _, test := range []struct {
		mode string
		want error
	}{
		{"malformed", ErrCompilerResponse},
		{"trailing", ErrCompilerResponse},
		{"nonzero", ErrCompilerInvocation},
		{"stderr", ErrCompilerInvocation},
	} {
		t.Run(test.mode, func(t *testing.T) {
			t.Setenv("PLYSTRA_DATA_TEST_COMPILER_CHILD", test.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, err := invokeSelectedCompiler(ctx, binary, protocol.AnalyzeRequest{})
			if !errors.Is(err, test.want) {
				t.Fatalf("child output error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestInvokeSelectedCompilerReadsOneFrame(t *testing.T) {
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PLYSTRA_DATA_TEST_COMPILER_CHILD", "frame")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	response, err := invokeSelectedCompiler(ctx, binary, protocol.AnalyzeRequest{})
	if err != nil || response.Schema != protocol.AnalyzeSchema {
		t.Fatalf("response = %#v, %v", response, err)
	}
}
