package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

func TestSSEMarshaler_ContentType(t *testing.T) {
	m := &SSEMarshaler{}
	if got := m.ContentType(nil); got != "text/event-stream" {
		t.Fatalf("ContentType() = %q, want text/event-stream", got)
	}
	if got := m.StreamContentType(nil); got != "text/event-stream" {
		t.Fatalf("StreamContentType() = %q, want text/event-stream", got)
	}
}

func TestSSEMarshaler_Delimiter(t *testing.T) {
	m := &SSEMarshaler{}
	if got := string(m.Delimiter()); got != "\n\n" {
		t.Fatalf("Delimiter() = %q, want %q", got, "\n\n")
	}
}

func TestSSEMarshaler_MarshalPrefixesDataNoTrailingNewline(t *testing.T) {
	m := &SSEMarshaler{}
	out, err := m.Marshal(map[string]any{"token": "hello"})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	s := string(out)
	if !strings.HasPrefix(s, "data: ") {
		t.Fatalf("missing 'data: ' prefix: %q", s)
	}
	if strings.HasSuffix(s, "\n") {
		t.Fatalf("Marshal must not append a newline (delimiter supplies framing): %q", s)
	}
	// The JSON after the prefix must be valid JSON with the original field.
	var got map[string]any
	if err := json.Unmarshal([]byte(strings.TrimPrefix(s, "data: ")), &got); err != nil {
		t.Fatalf("payload after prefix is not valid JSON: %v (%q)", err, s)
	}
	if got["token"] != "hello" {
		t.Fatalf("expected token=hello, got %v", got)
	}
}

func TestSSEMarshaler_EncoderProducesValidStream(t *testing.T) {
	m := &SSEMarshaler{}
	var buf bytes.Buffer
	enc := m.NewEncoder(&buf)
	if err := enc.Encode(map[string]any{"token": "a"}); err != nil {
		t.Fatalf("Encode #1: %v", err)
	}
	if err := enc.Encode(map[string]any{"token": "b"}); err != nil {
		t.Fatalf("Encode #2: %v", err)
	}

	want := `data: {"token":"a"}` + "\n\n" + `data: {"token":"b"}` + "\n\n"
	if got := buf.String(); got != want {
		t.Fatalf("encoder output mismatch:\n got: %q\nwant: %q", got, want)
	}
}

func TestSSEMarshaler_UnmarshalReturnsError(t *testing.T) {
	m := &SSEMarshaler{}
	err := m.Unmarshal([]byte("data: foo"), &struct{}{})
	if err == nil {
		t.Fatal("Unmarshal should return an error: SSE is a server-to-client format")
	}
	if !errors.Is(err, errSSEReadNotSupported) {
		t.Fatalf("Unmarshal err = %v, want errSSEReadNotSupported", err)
	}
}

func TestSSEMarshaler_DecoderReturnsError(t *testing.T) {
	m := &SSEMarshaler{}
	dec := m.NewDecoder(strings.NewReader("data: foo"))
	err := dec.Decode(&struct{}{})
	if err == nil {
		t.Fatal("Decoder should return an error: SSE is a server-to-client format")
	}
	if !errors.Is(err, errSSEReadNotSupported) {
		t.Fatalf("Decode err = %v, want errSSEReadNotSupported", err)
	}
}

// TestSSEMarshaler_SatisfiesGatewayInterfaces guards against a future
// refactor that drops one of the interfaces ForwardResponseStream relies on
// (Delimited for frame separators, StreamContentType for streams).
func TestSSEMarshaler_SatisfiesGatewayInterfaces(t *testing.T) {
	var (
		_ runtime.Marshaler         = (*SSEMarshaler)(nil)
		_ runtime.Delimited         = (*SSEMarshaler)(nil)
		_ runtime.StreamContentType = (*SSEMarshaler)(nil)
	)
}

// TestSSEMarshaler_NewEncoderPropagatesWriteError ensures stream cancellation
// (a broken pipe partway through encoding) surfaces to the handler rather
// than being silently swallowed.
func TestSSEMarshaler_NewEncoderPropagatesWriteError(t *testing.T) {
	m := &SSEMarshaler{}
	enc := m.NewEncoder(errWriter{})
	if err := enc.Encode(map[string]any{"token": "x"}); err == nil {
		t.Fatal("expected Encode to return write error")
	}
}

type errWriter struct{}

func (errWriter) Write(_ []byte) (int, error) { return 0, io.ErrClosedPipe }
