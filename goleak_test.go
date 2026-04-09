package core

import (
	"testing"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m,
		// gRPC callback serializers are started by resolver and balancer
		// components during dial/server setup. They are cleaned up when the
		// connection/server is closed, but some tests don't fully tear down.
		goleak.IgnoreTopFunction("google.golang.org/grpc/internal/grpcsync.(*CallbackSerializer).run"),
		// OTEL batch span processor runs a background queue for exporting
		// spans. Started by TracerProvider in tests that configure OTLP.
		goleak.IgnoreTopFunction("go.opentelemetry.io/otel/sdk/trace.(*batchSpanProcessor).processQueue"),
		// sentry-go starts batch processor and HTTP transport worker goroutines
		// when sentry.Init() is called in tests that set SentryDSN.
		goleak.IgnoreTopFunction("github.com/getsentry/sentry-go.(*batchProcessor[...]).run"),
		goleak.IgnoreTopFunction("github.com/getsentry/sentry-go.(*HTTPTransport).worker"),
		// rollbar-go creates a global async client at package init time
		// (rollbar.go:39: std = NewAsync(...)), starting a background goroutine
		// unconditionally when the package is imported. Cannot be avoided.
		goleak.IgnoreTopFunction("github.com/rollbar/rollbar-go.NewAsyncTransport.func1"),
	)
}
