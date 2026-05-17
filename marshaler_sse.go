package core

import (
	"errors"
	"io"

	"github.com/go-coldbrew/core/config"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
)

// buildHTTPMuxOptions assembles the base runtime.ServeMuxOptions applied to
// the HTTP gateway, in the order grpc-gateway applies them: defaults first,
// then config-toggled options (SSE marshaler, JSON builtin), then
// service-registered options on top (appended by the caller). Service options
// win on the same MIME so they can override SSEMarshaler with a custom
// variant if needed.
func buildHTTPMuxOptions(cfg config.Config, allowedHeaderPrefixes []string, protoMarshaler runtime.Marshaler) []runtime.ServeMuxOption {
	opts := []runtime.ServeMuxOption{
		runtime.WithIncomingHeaderMatcher(
			getCustomHeaderMatcher(allowedHeaderPrefixes, cfg.TraceHeaderName, cfg.DebugLogHeaderName),
		),
		runtime.WithMarshalerOption("application/proto", protoMarshaler),
		runtime.WithMarshalerOption("application/protobuf", protoMarshaler),
		runtime.WithMiddlewares(spanRouteMiddleware),
	}
	if !cfg.DisableSSEMarshaler {
		opts = append(opts, runtime.WithMarshalerOption(sseMediaType, &SSEMarshaler{}))
	}
	if cfg.UseJSONBuiltinMarshaller {
		opts = append(opts, runtime.WithMarshalerOption(cfg.JSONBuiltinMarshallerMime, &runtime.JSONBuiltin{}))
	}
	return opts
}

// SSEMarshaler is a runtime.Marshaler that emits Server-Sent Events
// (text/event-stream) frames for server-streaming gateway RPCs. It lets
// browser EventSource clients consume streaming RPCs directly — useful for
// AI/LLM token streaming and other long-running progressive responses.
//
// Each Marshal call returns "data: <json>" with no trailing newline; the
// Delimiter ("\n\n") terminates each SSE frame per the SSE spec. The JSON
// payload uses protojson via the embedded runtime.JSONPb, so field naming
// matches the gateway's default JSON responses.
//
// Wire it up from a service's PreStart hook:
//
//	core.RegisterHTTPMarshaler("text/event-stream", &core.SSEMarshaler{})
//
// Clients then opt in by sending Accept: text/event-stream on the gateway
// URL. The newHTTPCompressionWrapper excludes text/event-stream from
// gzip/zstd compression so frames reach the client in real time (compressed
// SSE is buffered by many HTTP intermediaries).
//
// SSE is server-to-client only: Unmarshal and NewDecoder return an error.
//
// Per-field protojson options (EmitUnpopulated, UseProtoNames, etc.) can be
// set by initializing the embedded JSONPb directly:
//
//	&core.SSEMarshaler{JSONPb: runtime.JSONPb{
//	    MarshalOptions: protojson.MarshalOptions{EmitUnpopulated: true},
//	}}
type SSEMarshaler struct {
	runtime.JSONPb
}

var (
	ssePrefix              = []byte("data: ")
	sseDelimiter           = []byte("\n\n")
	errSSEReadNotSupported = errors.New("core: SSEMarshaler does not support reading; Server-Sent Events is a server-to-client format")
)

// ContentType always returns "text/event-stream".
func (*SSEMarshaler) ContentType(_ any) string {
	return sseMediaType
}

// StreamContentType matches ContentType so server-streaming responses also
// advertise text/event-stream. Gateway prefers this over ContentType when
// implemented (see runtime.ForwardResponseStream).
func (*SSEMarshaler) StreamContentType(_ any) string {
	return sseMediaType
}

// Marshal returns "data: <json>" with no trailing newline. Frame
// termination is supplied by Delimiter; the gateway writes Marshal output
// followed by Delimiter for each streamed message.
func (s *SSEMarshaler) Marshal(v any) ([]byte, error) {
	body, err := s.JSONPb.Marshal(v)
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(ssePrefix)+len(body))
	out = append(out, ssePrefix...)
	out = append(out, body...)
	return out, nil
}

// Delimiter returns "\n\n", which terminates one SSE frame.
func (*SSEMarshaler) Delimiter() []byte {
	return sseDelimiter
}

// Unmarshal returns an error: SSE is a server-to-client format and the
// gateway never reads SSE bodies from inbound requests.
func (*SSEMarshaler) Unmarshal(_ []byte, _ any) error {
	return errSSEReadNotSupported
}

// NewDecoder returns a decoder that always errors, for the same reason as
// Unmarshal.
func (*SSEMarshaler) NewDecoder(_ io.Reader) runtime.Decoder {
	return runtime.DecoderFunc(func(_ any) error {
		return errSSEReadNotSupported
	})
}

// NewEncoder returns an encoder that writes "data: <json>\n\n" per Encode
// call.
func (s *SSEMarshaler) NewEncoder(w io.Writer) runtime.Encoder {
	return runtime.EncoderFunc(func(v any) error {
		body, err := s.Marshal(v)
		if err != nil {
			return err
		}
		if _, err := w.Write(body); err != nil {
			return err
		}
		_, err = w.Write(s.Delimiter())
		return err
	})
}
