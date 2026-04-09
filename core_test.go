package core

import (
	"context"
	"testing"

	"github.com/go-coldbrew/core/config"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/grpc"
)

// mockCBService is a minimal implementation of CBService for testing.
type mockCBService struct{}

func (m *mockCBService) InitHTTP(_ context.Context, _ *runtime.ServeMux, _ string, _ []grpc.DialOption) error {
	return nil
}

func (m *mockCBService) InitGRPC(_ context.Context, _ *grpc.Server) error {
	return nil
}

func TestNew(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	cb := New(config.Config{
		DisableSignalHandler: true,
	})
	if cb == nil {
		t.Fatal("New() returned nil")
	}
}

func TestSetService_Nil(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		svc: make([]CBService, 0),
	}
	err := c.SetService(nil)
	if err == nil {
		t.Fatal("SetService(nil) should return an error")
	}
	if err.Error() != "service is nil" {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestSetService_Valid(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		svc: make([]CBService, 0),
	}
	err := c.SetService(&mockCBService{})
	if err != nil {
		t.Fatalf("SetService with valid service returned error: %v", err)
	}
	if len(c.svc) != 1 {
		t.Fatalf("expected 1 service, got %d", len(c.svc))
	}
}

func TestParseHeaders(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	tests := []struct {
		name     string
		input    string
		expected map[string]string
	}{
		{
			name:     "empty string",
			input:    "",
			expected: map[string]string{},
		},
		{
			name:  "single pair",
			input: "key1=value1",
			expected: map[string]string{
				"key1": "value1",
			},
		},
		{
			name:  "multiple pairs",
			input: "key1=value1,key2=value2",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
		{
			name:     "malformed pair is ignored",
			input:    "noequals",
			expected: map[string]string{},
		},
		{
			name:  "whitespace is trimmed",
			input: " key1 = value1 , key2 = value2 ",
			expected: map[string]string{
				"key1": "value1",
				"key2": "value2",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// removed t.Parallel() — core tests mutate package-level globals
			result := parseHeaders(tc.input)
			if len(result) != len(tc.expected) {
				t.Fatalf("expected %d entries, got %d: %v", len(tc.expected), len(result), result)
			}
			for k, v := range tc.expected {
				got, ok := result[k]
				if !ok {
					t.Fatalf("missing key %q", k)
				}
				if got != v {
					t.Fatalf("key %q: expected %q, got %q", k, v, got)
				}
			}
		})
	}
}

func TestGetCustomHeaderMatcher(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals

	matcher := getCustomHeaderMatcher([]string{"X-Custom-"}, "X-Trace-Id")

	t.Run("matching trace header returns true", func(t *testing.T) {
		// removed t.Parallel() — core tests mutate package-level globals
		key, matched := matcher("X-Trace-Id")
		if !matched {
			t.Fatal("expected trace header to match")
		}
		if key != "x-trace-id" {
			t.Fatalf("expected key %q, got %q", "x-trace-id", key)
		}
	})

	t.Run("matching prefix returns true", func(t *testing.T) {
		// removed t.Parallel() — core tests mutate package-level globals
		key, matched := matcher("X-Custom-Something")
		if !matched {
			t.Fatal("expected prefixed header to match")
		}
		if key != "x-custom-something" {
			t.Fatalf("expected key %q, got %q", "x-custom-something", key)
		}
	})

	t.Run("non-matching header falls back to default matcher", func(t *testing.T) {
		// removed t.Parallel() — core tests mutate package-level globals
		// "Content-Type" is handled by DefaultHeaderMatcher
		key, matched := matcher("Content-Type")
		if !matched {
			t.Fatal("expected Content-Type to be matched by default matcher")
		}
		if key == "" {
			t.Fatal("expected non-empty key for Content-Type")
		}
		// Unknown non-prefixed header should not match
		_, matched2 := matcher("X-Unknown-Non-Prefixed")
		if matched2 {
			t.Fatal("expected non-matching header to not match")
		}
	})
}

func TestGetCustomHeaderMatcher_MultipleHeaders(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	matcher := getCustomHeaderMatcher(nil, "X-Trace-Id", "X-Debug-Log-Level")

	_, traceMatched := matcher("X-Trace-Id")
	if !traceMatched {
		t.Fatal("expected trace header to match")
	}
	_, debugMatched := matcher("X-Debug-Log-Level")
	if !debugMatched {
		t.Fatal("expected debug log header to match")
	}
	_, unknownMatched := matcher("X-Random")
	if unknownMatched {
		t.Fatal("expected unknown header to not match")
	}
}
