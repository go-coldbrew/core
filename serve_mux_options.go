package core

import "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"

// httpServeMuxOptions accumulates options registered via
// RegisterServeMuxOption / RegisterHTTPMarshaler. Init-only — not safe for
// concurrent registration. Matches the existing config-function pattern in
// the interceptors package; the alternative (mutex/atomic) would diverge
// from the rest of the codebase for no real benefit at startup.
var httpServeMuxOptions []runtime.ServeMuxOption

// RegisterServeMuxOption appends a runtime.ServeMuxOption that initHTTP
// passes to runtime.NewServeMux. Registered options are applied AFTER core's
// built-ins (the incoming-header matcher derived from HTTPHeaderPrefixes,
// the application/proto and application/protobuf marshalers, and the
// span-route middleware), so:
//
//   - Last-write-wins options — WithMarshalerOption for a given MIME,
//     WithErrorHandler, WithRoutingErrorHandler, WithIncomingHeaderMatcher —
//     can intentionally override core's defaults. Overriding the incoming
//     header matcher disables the HTTPHeaderPrefixes wiring; reimplement it
//     yourself if you still need that behavior.
//   - Additive options — WithMiddlewares, WithMetadata,
//     WithForwardResponseOption — stack with core's.
//
// Must be called before core.Run() (typically from a service's PreStart
// hook). Not safe for concurrent registration.
func RegisterServeMuxOption(opt runtime.ServeMuxOption) {
	httpServeMuxOptions = append(httpServeMuxOptions, opt)
}

// RegisterHTTPMarshaler registers a runtime.Marshaler for the given MIME
// type on the HTTP gateway. Equivalent to
// RegisterServeMuxOption(runtime.WithMarshalerOption(mime, m)).
//
// To override the gateway's default fallback for unregistered Content-Types
// (which is protojson via runtime.JSONPb), register for runtime.MIMEWildcard.
//
// Must be called before core.Run(). Not safe for concurrent registration.
func RegisterHTTPMarshaler(mime string, m runtime.Marshaler) {
	RegisterServeMuxOption(runtime.WithMarshalerOption(mime, m))
}

func registeredServeMuxOptions() []runtime.ServeMuxOption {
	return httpServeMuxOptions
}

func resetServeMuxOptionsForTest() {
	httpServeMuxOptions = nil
}
