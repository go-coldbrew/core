package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/go-coldbrew/core/config"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
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

// TestSSEMarshaler_DelimiterReturnsFreshSlice guards against accidental
// sharing of the underlying array: mutating the returned slice must not
// change the framing the next caller sees.
func TestSSEMarshaler_DelimiterReturnsFreshSlice(t *testing.T) {
	m := &SSEMarshaler{}
	first := m.Delimiter()
	first[0] = 'X'
	if got := string(m.Delimiter()); got != "\n\n" {
		t.Fatalf("Delimiter mutated by previous caller: got %q, want %q", got, "\n\n")
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

// TestSSEMarshaler_MultilinePayloadPrefixesEveryLine guards the SSE
// continuation behavior: callers that opt into multiline JSON (via
// protojson.MarshalOptions.Multiline/Indent on the embedded JSONPb) get a
// frame where every payload line starts with "data: ". Without this,
// EventSource truncates the frame at the first newline.
func TestSSEMarshaler_MultilinePayloadPrefixesEveryLine(t *testing.T) {
	m := &SSEMarshaler{
		JSONPb: runtime.JSONPb{
			MarshalOptions: protojson.MarshalOptions{Multiline: true, Indent: "  "},
		},
	}
	msg, err := structpb.NewStruct(map[string]any{"token": "hello", "index": 0})
	if err != nil {
		t.Fatalf("structpb.NewStruct: %v", err)
	}

	out, err := m.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	s := string(out)
	if !strings.HasPrefix(s, "data: ") {
		t.Fatalf("missing 'data: ' prefix: %q", s)
	}
	// Every internal newline in the payload must be followed by a "data: "
	// continuation prefix. If we strip the leading prefix, no bare newline
	// should be left unprefixed.
	body := strings.TrimPrefix(s, "data: ")
	for i := 0; i < len(body); {
		idx := strings.IndexByte(body[i:], '\n')
		if idx < 0 {
			break
		}
		after := body[i+idx+1:]
		if !strings.HasPrefix(after, "data: ") {
			t.Fatalf("payload contains a bare newline not followed by 'data: ' continuation prefix\nfull output:\n%s", s)
		}
		i += idx + 1
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

// TestBuildHTTPMuxOptions_SSERegisteredByDefault confirms that an HTTP gateway
// built with default config selects SSEMarshaler when the client sends
// Accept: text/event-stream. Without this, SSE clients would silently fall
// back to the JSON marshaler and never receive event-stream framing.
func TestBuildHTTPMuxOptions_SSERegisteredByDefault(t *testing.T) {
	mux := runtime.NewServeMux(
		buildHTTPMuxOptions(config.Config{}, nil, &runtime.ProtoMarshaller{})...,
	)

	req, _ := http.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("Accept", "text/event-stream")

	_, outbound := runtime.MarshalerForRequest(mux, req)
	if _, ok := outbound.(*SSEMarshaler); !ok {
		t.Fatalf("expected outbound marshaler to be *SSEMarshaler for Accept: text/event-stream, got %T", outbound)
	}
}

// TestBuildHTTPMuxOptions_SSEDisabled confirms that setting
// DisableSSEMarshaler suppresses the auto-registration so the gateway falls
// back to the default JSON marshaler. Important so services that explicitly
// don't want SSE — or want to register a custom SSE marshaler — get a clean
// slate.
func TestBuildHTTPMuxOptions_SSEDisabled(t *testing.T) {
	mux := runtime.NewServeMux(
		buildHTTPMuxOptions(config.Config{DisableSSEMarshaler: true}, nil, &runtime.ProtoMarshaller{})...,
	)

	req, _ := http.NewRequest(http.MethodGet, "/anything", nil)
	req.Header.Set("Accept", "text/event-stream")

	_, outbound := runtime.MarshalerForRequest(mux, req)
	if _, ok := outbound.(*SSEMarshaler); ok {
		t.Fatal("expected SSEMarshaler not to be registered when DisableSSEMarshaler is true")
	}
}
