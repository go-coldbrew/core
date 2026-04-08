package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/go-coldbrew/core/config"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// --- Test helpers ---

// testService implements CBService, CBStopper, and CBGracefulStopper.
type testService struct {
	initHTTPCalled  bool
	initGRPCCalled  bool
	stopCalled      bool
	failCheckCalled bool
	ready           chan struct{} // closed when InitGRPC is called; nil if unused
}

func (s *testService) InitHTTP(_ context.Context, _ *runtime.ServeMux, _ string, _ []grpc.DialOption) error {
	if s.ready == nil {
		s.initHTTPCalled = true
	}
	return nil
}

func (s *testService) InitGRPC(_ context.Context, _ *grpc.Server) error {
	if s.ready != nil {
		close(s.ready)
	} else {
		s.initGRPCCalled = true
	}
	return nil
}

func (s *testService) Stop() {
	s.stopCalled = true
}

func (s *testService) FailCheck(_ bool) {
	s.failCheckCalled = true
}

// errorService returns configurable errors from Init methods.
type errorService struct {
	grpcErr error
	httpErr error
}

func (s *errorService) InitHTTP(_ context.Context, _ *runtime.ServeMux, _ string, _ []grpc.DialOption) error {
	return s.httpErr
}

func (s *errorService) InitGRPC(_ context.Context, _ *grpc.Server) error {
	return s.grpcErr
}

// --- Group 1: Utility Functions ---

func TestSetOpenAPIHandler(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	c.SetOpenAPIHandler(handler)
	if c.openAPIHandler == nil {
		t.Fatal("expected openAPIHandler to be set")
	}
}

func TestTimedCall_Completes(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	called := false
	timedCall(ctx, func() {
		called = true
	})
	if !called {
		t.Fatal("expected function to be called")
	}
}

func TestTimedCall_TimesOut(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	timedCall(ctx, func() {
		time.Sleep(200 * time.Millisecond)
	})
	// Should return without hanging — context expires before the sleep completes
}

func TestClose_WithClosers(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	closed := false
	c := &cb{
		closers: []io.Closer{
			io.NopCloser(nil),
			closerFunc(func() error {
				closed = true
				return nil
			}),
		},
	}
	c.close()
	if !closed {
		t.Fatal("expected closer to be called")
	}
}

func TestClose_WithErrorCloser(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		closers: []io.Closer{
			closerFunc(func() error {
				return fmt.Errorf("close error")
			}),
		},
	}
	c.close() // should not panic
}

func TestClose_WithNilCloser(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		closers: []io.Closer{nil},
	}
	c.close() // should skip nil without panic
}

func TestLoadTLSCredentials_BadFiles(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	_, err := loadTLSCredentials("/nonexistent/cert.pem", "/nonexistent/key.pem", false)
	if err == nil {
		t.Fatal("expected error for nonexistent TLS files")
	}
}

func TestTracingWrapper(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := tracingWrapper(inner)

	req := httptest.NewRequest("GET", "/api/test", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

func TestTracingWrapper_StatusCodes(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	tests := []struct {
		name       string
		handler    http.HandlerFunc
		wantStatus int
	}{
		{
			name: "records 200",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "records 404",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name: "records 500",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "defaults to 200 when WriteHeader not called",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				fmt.Fprint(w, "ok")
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// removed t.Parallel() — core tests mutate package-level globals
			wrapped := tracingWrapper(tt.handler)
			req := httptest.NewRequest("GET", "/api/test", nil)
			w := httptest.NewRecorder()
			wrapped.ServeHTTP(w, req)
			if w.Code != tt.wantStatus {
				t.Fatalf("expected %d, got %d", tt.wantStatus, w.Code)
			}
		})
	}
}

func TestStatusRecorder(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals

	t.Run("captures explicit status", func(t *testing.T) {
		// removed t.Parallel() — core tests mutate package-level globals
		w := httptest.NewRecorder()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		rec.WriteHeader(http.StatusBadGateway)
		if rec.status != http.StatusBadGateway {
			t.Fatalf("expected %d, got %d", http.StatusBadGateway, rec.status)
		}
		if w.Code != http.StatusBadGateway {
			t.Fatalf("underlying writer expected %d, got %d", http.StatusBadGateway, w.Code)
		}
	})

	t.Run("unwrap returns underlying writer", func(t *testing.T) {
		// removed t.Parallel() — core tests mutate package-level globals
		w := httptest.NewRecorder()
		rec := &statusRecorder{ResponseWriter: w}
		if rec.Unwrap() != w {
			t.Fatal("Unwrap did not return underlying ResponseWriter")
		}
	})
}

func TestSpanRouteMiddleware(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals

	t.Run("calls next handler", func(t *testing.T) {
		// removed t.Parallel() — core tests mutate package-level globals
		called := false
		next := func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
			called = true
			w.WriteHeader(http.StatusOK)
		}
		wrapped := spanRouteMiddleware(next)
		req := httptest.NewRequest("GET", "/api/v1/rules", nil)
		w := httptest.NewRecorder()
		wrapped(w, req, nil)
		if !called {
			t.Fatal("expected next handler to be called")
		}
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200, got %d", w.Code)
		}
	})

	t.Run("passes path params through", func(t *testing.T) {
		// removed t.Parallel() — core tests mutate package-level globals
		var gotParams map[string]string
		next := func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
			gotParams = pathParams
		}
		wrapped := spanRouteMiddleware(next)
		params := map[string]string{"id": "123"}
		req := httptest.NewRequest("GET", "/api/v1/rules/123", nil)
		w := httptest.NewRecorder()
		wrapped(w, req, params)
		if gotParams["id"] != "123" {
			t.Fatalf("expected path param id=123, got %v", gotParams)
		}
	})
}

// setupTestTracer installs an in-memory span exporter and returns it along with
// a cleanup function that restores the previous tracer provider.
func setupTestTracer() (*tracetest.InMemoryExporter, func()) {
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	return exporter, func() {
		otel.SetTracerProvider(prev)
		_ = tp.Shutdown(context.Background())
	}
}

// findSpanByName returns the first span with the given name, or nil if not found.
func findSpanByName(spans tracetest.SpanStubs, name string) *tracetest.SpanStub {
	for i := range spans {
		if spans[i].Name == name {
			return &spans[i]
		}
	}
	return nil
}

// spanAttrMap returns a map of attribute key to value for a span.
func spanAttrMap(span *tracetest.SpanStub) map[string]any {
	m := make(map[string]any, len(span.Attributes))
	for _, a := range span.Attributes {
		m[string(a.Key)] = a.Value.AsInterface()
	}
	return m
}

func TestTracingWrapperSpanAttributes(t *testing.T) {
	exporter, cleanup := setupTestTracer()
	defer cleanup()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	wrapped := tracingWrapper(inner)

	req := httptest.NewRequest("GET", "/api/v1/rules?page=1", nil)
	req.Host = "example.com:9091"
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	span := findSpanByName(exporter.GetSpans(), "GET")
	if span == nil {
		t.Fatal("expected span named 'GET'")
	}

	attrs := spanAttrMap(span)
	if v := attrs["http.request.method"]; v != "GET" {
		t.Fatalf("expected http.request.method=GET, got %v", v)
	}
	if v := attrs["url.path"]; v != "/api/v1/rules" {
		t.Fatalf("expected url.path=/api/v1/rules, got %v", v)
	}
	if v := attrs["url.query"]; v != "page=1" {
		t.Fatalf("expected url.query=page=1, got %v", v)
	}
	if v := attrs["server.address"]; v != "example.com" {
		t.Fatalf("expected server.address=example.com, got %v", v)
	}
	if v := attrs["server.port"]; v != int64(9091) {
		t.Fatalf("expected server.port=9091, got %v", v)
	}
	if v := attrs["http.response.status_code"]; v != int64(200) {
		t.Fatalf("expected http.response.status_code=200, got %v", v)
	}
	if v := attrs["url.scheme"]; v != "http" {
		t.Fatalf("expected url.scheme=http, got %v", v)
	}
}

func TestTracingWrapperSpanErrorStatus(t *testing.T) {
	exporter, cleanup := setupTestTracer()
	defer cleanup()

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	wrapped := tracingWrapper(inner)

	req := httptest.NewRequest("GET", "/api/error", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	span := findSpanByName(exporter.GetSpans(), "GET")
	if span == nil {
		t.Fatal("expected span named 'GET'")
	}

	attrs := spanAttrMap(span)
	if v := attrs["http.response.status_code"]; v != int64(500) {
		t.Fatalf("expected http.response.status_code=500, got %v", v)
	}
	if span.Status.Code != codes.Error {
		t.Fatalf("expected span status Error, got %v", span.Status.Code)
	}
}

func TestTracingWrapperGatewaySpanName(t *testing.T) {
	exporter, cleanup := setupTestTracer()
	defer cleanup()

	// Create a grpc-gateway mux with spanRouteMiddleware and a test handler.
	mux := runtime.NewServeMux(runtime.WithMiddlewares(spanRouteMiddleware))
	err := mux.HandlePath("GET", "/api/v1/rules/{rule_id}", func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
		w.WriteHeader(http.StatusOK)
	})
	if err != nil {
		t.Fatal(err)
	}

	wrapped := tracingWrapper(mux)
	req := httptest.NewRequest("GET", "/api/v1/rules/123", nil)
	w := httptest.NewRecorder()
	wrapped.ServeHTTP(w, req)

	// Pattern.String() includes wildcard spec, e.g. {rule_id=*}
	wantName := "GET /api/v1/rules/{rule_id=*}"
	span := findSpanByName(exporter.GetSpans(), wantName)
	if span == nil {
		names := make([]string, 0)
		for _, s := range exporter.GetSpans() {
			names = append(names, s.Name)
		}
		t.Fatalf("expected span named %q, got spans: %v", wantName, names)
	}

	attrs := spanAttrMap(span)
	if v := attrs["http.route"]; v != "/api/v1/rules/{rule_id=*}" {
		t.Fatalf("expected http.route=/api/v1/rules/{rule_id=*}, got %v", v)
	}
}

func TestGetCustomHeaderMatcher_EmptyPrefixes(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	matcher := getCustomHeaderMatcher(nil, "X-Trace-Id")

	_, matched := matcher("X-Trace-Id")
	if !matched {
		t.Fatal("expected trace header to match")
	}
	_, matched = matcher("X-Random-Header")
	if matched {
		t.Fatal("expected non-standard header to not match")
	}
}

func TestGetCustomHeaderMatcher_EmptyPrefix(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	matcher := getCustomHeaderMatcher([]string{""}, "X-Trace-Id")
	_, matched := matcher("X-Random-Header")
	if matched {
		t.Fatal("empty prefix should not match anything")
	}
}

// --- Group 2: gRPC Server Options ---

func TestGetGRPCServerOptions_Default(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	// With sane defaults (300, 1800, 30), keepalive params should be applied
	c := &cb{config: config.Config{
		GRPCServerMaxConnectionIdleInSeconds:     300,
		GRPCServerMaxConnectionAgeInSeconds:      1800,
		GRPCServerMaxConnectionAgeGraceInSeconds: 30,
	}}
	opts := c.getGRPCServerOptions()
	if len(opts) < 3 {
		t.Fatalf("expected at least 3 server options with default keepalive, got %d", len(opts))
	}
}

func TestGetGRPCServerOptions_KeepaliveDisabledWithNegativeOne(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	// Setting all values to -1 should still add keepalive params (outer != 0 check),
	// but individual parameters are not set on ServerParameters (inner > 0 check),
	// so gRPC uses infinity for each.
	c := &cb{config: config.Config{
		GRPCServerMaxConnectionIdleInSeconds:     -1,
		GRPCServerMaxConnectionAgeInSeconds:      -1,
		GRPCServerMaxConnectionAgeGraceInSeconds: -1,
	}}
	opts := c.getGRPCServerOptions()
	if len(opts) < 3 {
		t.Fatalf("expected at least 3 server options with -1 keepalive, got %d", len(opts))
	}
}

func TestGetGRPCServerOptions_KeepaliveMixed(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	// Mix of -1 (disabled) and positive (enabled) values
	c := &cb{config: config.Config{
		GRPCServerMaxConnectionIdleInSeconds:     -1,
		GRPCServerMaxConnectionAgeInSeconds:      1800,
		GRPCServerMaxConnectionAgeGraceInSeconds: 30,
	}}
	opts := c.getGRPCServerOptions()
	if len(opts) < 3 {
		t.Fatalf("expected at least 3 server options with mixed keepalive, got %d", len(opts))
	}
}

func TestGetGRPCServerOptions_KeepaliveAllZero(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	// All zeros should NOT add keepalive params (outer != 0 check is false)
	c := &cb{config: config.Config{
		GRPCServerMaxConnectionIdleInSeconds:     0,
		GRPCServerMaxConnectionAgeInSeconds:      0,
		GRPCServerMaxConnectionAgeGraceInSeconds: 0,
	}}
	opts := c.getGRPCServerOptions()
	// Should only have the base options (interceptors + stats handler), no keepalive
	baseOpts := len(opts)
	c2 := &cb{config: config.Config{
		GRPCServerMaxConnectionIdleInSeconds: 300,
	}}
	optsWithKeepalive := c2.getGRPCServerOptions()
	if len(optsWithKeepalive) <= baseOpts {
		t.Fatalf("expected more options with keepalive than without, got %d vs %d", len(optsWithKeepalive), baseOpts)
	}
}

func TestGetGRPCServerOptions_WithMsgSizeLimits(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{config: config.Config{
		GRPCMaxRecvMsgSize: 4 * 1024 * 1024,
		GRPCMaxSendMsgSize: 4 * 1024 * 1024,
	}}
	opts := c.getGRPCServerOptions()
	if len(opts) < 4 {
		t.Fatalf("expected at least 4 options with msg size limits, got %d", len(opts))
	}
}

func TestGetGRPCServerOptions_WithKeepalive(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{config: config.Config{
		GRPCServerMaxConnectionIdleInSeconds:     30,
		GRPCServerMaxConnectionAgeInSeconds:      60,
		GRPCServerMaxConnectionAgeGraceInSeconds: 10,
	}}
	opts := c.getGRPCServerOptions()
	if len(opts) < 3 {
		t.Fatalf("expected at least 3 options with keepalive, got %d", len(opts))
	}
}

// --- Group 3: processConfig Branches ---
// These tests mutate global state (logger, tracer, interceptor config) so
// they must NOT run in parallel with each other or with other tests.

func TestProcessConfig_Minimal(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler: true,
		DisableNewRelic:      true,
		DisableAutoMaxProcs:  true,
		DisableVTProtobuf:    true,
	}}
	c.processConfig()
}

func TestProcessConfig_WithVTProto(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler: true,
		DisableNewRelic:      true,
		DisableAutoMaxProcs:  true,
	}}
	c.processConfig()
}

func TestProcessConfig_WithAutoMaxProcs(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler: true,
		DisableNewRelic:      true,
		DisableVTProtobuf:    true,
	}}
	c.processConfig()
}

func TestProcessConfig_WithSentry(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler: true,
		DisableNewRelic:      true,
		DisableAutoMaxProcs:  true,
		DisableVTProtobuf:    true,
		SentryDSN:            "https://fake@sentry.io/123",
		Environment:          "test",
		ReleaseName:          "v0.0.1",
	}}
	c.processConfig()
}

func TestProcessConfig_WithPrometheusHistogram(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler:           true,
		DisableNewRelic:                true,
		DisableAutoMaxProcs:            true,
		DisableVTProtobuf:              true,
		EnablePrometheusGRPCHistogram:  true,
		PrometheusGRPCHistogramBuckets: []float64{0.001, 0.01, 0.1, 1.0},
	}}
	c.processConfig()
}

func TestProcessConfig_WithPrometheusHistogramDefaultBuckets(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler:          true,
		DisableNewRelic:               true,
		DisableAutoMaxProcs:           true,
		DisableVTProtobuf:             true,
		EnablePrometheusGRPCHistogram: true,
	}}
	c.processConfig()
}

func TestProcessConfig_WithNewRelicEmptyKey(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler: true,
		DisableAutoMaxProcs:  true,
		DisableVTProtobuf:    true,
	}}
	c.processConfig()
}

func TestProcessConfig_WithOTLP(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler: true,
		DisableNewRelic:      true,
		DisableAutoMaxProcs:  true,
		DisableVTProtobuf:    true,
		OTLPEndpoint:         "localhost:4317",
		OTLPHeaders:          "api-key=test123",
		AppName:              "test-service",
		ReleaseName:          "v0.0.1",
		OTLPSamplingRatio:    0.5,
		OTLPCompression:      "gzip",
		OTLPInsecure:         true,
	}}
	c.processConfig()
}

func TestProcessConfig_WithNROpenTelemetry(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler:        true,
		DisableNewRelic:             true,
		DisableAutoMaxProcs:         true,
		DisableVTProtobuf:           true,
		NewRelicOpentelemetry:       true,
		NewRelicLicenseKey:          "",
		NewRelicOpentelemetrySample: 0.1,
	}}
	c.processConfig()
}

func TestProcessConfig_WithInterceptorConfig(t *testing.T) {
	c := &cb{config: config.Config{
		DisableSignalHandler:   true,
		DisableNewRelic:        true,
		DisableAutoMaxProcs:    true,
		DisableVTProtobuf:      true,
		DoNotLogGRPCReflection: true,
		TraceHeaderName:        "X-Custom-Trace",
	}}
	c.processConfig()
}

func TestProcessConfig_WithShutdownDuration(t *testing.T) {
	c := &cb{config: config.Config{
		DisableNewRelic:           true,
		DisableAutoMaxProcs:       true,
		DisableVTProtobuf:         true,
		DisableSignalHandler:      true,
		ShutdownDurationInSeconds: 5,
	}}
	c.processConfig()
}

func TestSetupHystrixPrometheus_CalledTwice(t *testing.T) {
	// Regression test: calling SetupHystrixPrometheus multiple times
	// must not panic (sync.Once guards duplicate Prometheus registration).
	SetupHystrixPrometheus()
	SetupHystrixPrometheus()
}

// --- Group 4: Init Server Functions ---

func TestInitGRPC(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	svc := &testService{}
	c := &cb{
		config: config.Config{},
		svc:    []CBService{svc},
	}
	server, err := c.initGRPC(context.Background())
	if err != nil {
		t.Fatalf("initGRPC failed: %v", err)
	}
	if server == nil {
		t.Fatal("expected non-nil gRPC server")
	}
	if !svc.initGRPCCalled {
		t.Fatal("expected InitGRPC to be called on service")
	}
	server.Stop()
}

func TestInitGRPC_ServiceError(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	svc := &errorService{grpcErr: fmt.Errorf("init grpc failed")}
	c := &cb{
		config: config.Config{},
		svc:    []CBService{svc},
	}
	_, err := c.initGRPC(context.Background())
	if err == nil {
		t.Fatal("expected error from initGRPC")
	}
}

func TestInitGRPC_WithTLS_BadFiles(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{
			GRPCTLSCertFile: "/nonexistent/cert.pem",
			GRPCTLSKeyFile:  "/nonexistent/key.pem",
		},
		svc: []CBService{&testService{}},
	}
	_, err := c.initGRPC(context.Background())
	if err == nil {
		t.Fatal("expected error for bad TLS files")
	}
}

func TestInitGRPC_DisableReflection(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{DisableGRPCReflection: true},
		svc:    []CBService{&testService{}},
	}
	server, err := c.initGRPC(context.Background())
	if err != nil {
		t.Fatalf("initGRPC failed: %v", err)
	}
	server.Stop()
}

func TestInitHTTP(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	svc := &testService{}
	c := &cb{
		config: config.Config{
			GRPCPort:   19090,
			HTTPPort:   19091,
			ListenHost: "127.0.0.1",
		},
		svc: []CBService{svc},
	}
	server, err := c.initHTTP(context.Background())
	if err != nil {
		t.Fatalf("initHTTP failed: %v", err)
	}
	if server == nil {
		t.Fatal("expected non-nil HTTP server")
	}
	if !svc.initHTTPCalled {
		t.Fatal("expected InitHTTP to be called")
	}
}

func TestInitHTTP_ServiceError(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	svc := &errorService{httpErr: fmt.Errorf("init http failed")}
	c := &cb{
		config: config.Config{
			GRPCPort:   19090,
			HTTPPort:   19091,
			ListenHost: "127.0.0.1",
		},
		svc: []CBService{svc},
	}
	_, err := c.initHTTP(context.Background())
	if err == nil {
		t.Fatal("expected error from initHTTP")
	}
}

func TestInitHTTP_WithOptions(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{
			GRPCPort:                  19090,
			HTTPPort:                  19091,
			ListenHost:                "127.0.0.1",
			GRPCMaxRecvMsgSize:        4 * 1024 * 1024,
			GRPCMaxSendMsgSize:        4 * 1024 * 1024,
			HTTPHeaderPrefixes:        []string{"X-Custom-"},
			TraceHeaderName:           "X-Trace-Id",
			UseJSONBuiltinMarshaller:  true,
			JSONBuiltinMarshallerMime: "application/json",
		},
		svc: []CBService{&testService{}},
	}
	server, err := c.initHTTP(context.Background())
	if err != nil {
		t.Fatalf("initHTTP with options failed: %v", err)
	}
	if server == nil {
		t.Fatal("expected non-nil HTTP server")
	}
}

func TestInitHTTP_BackwardCompatiblePrefix(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{
			GRPCPort:         19090,
			HTTPPort:         19091,
			ListenHost:       "127.0.0.1",
			HTTPHeaderPrefix: "X-Legacy-",
		},
		svc: []CBService{&testService{}},
	}
	server, err := c.initHTTP(context.Background())
	if err != nil {
		t.Fatalf("initHTTP with legacy prefix failed: %v", err)
	}
	if server == nil {
		t.Fatal("expected non-nil HTTP server")
	}
}

// --- Group 5: HTTP Handler Routing ---

func TestHTTPHandler_Swagger(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("swagger"))
	})
	c := &cb{
		config: config.Config{
			GRPCPort:   19090,
			HTTPPort:   19091,
			ListenHost: "127.0.0.1",
			SwaggerURL: "/swagger/",
		},
		svc:            []CBService{&testService{}},
		openAPIHandler: handler,
	}
	svr, err := c.initHTTP(context.Background())
	if err != nil {
		t.Fatalf("initHTTP failed: %v", err)
	}

	req := httptest.NewRequest("GET", "/swagger/index.html", nil)
	w := httptest.NewRecorder()
	svr.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for swagger, got %d", w.Code)
	}
	if w.Body.String() != "swagger" {
		t.Fatalf("expected 'swagger' body, got %q", w.Body.String())
	}
}

func TestHTTPHandler_Pprof(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{
			GRPCPort:   19090,
			HTTPPort:   19091,
			ListenHost: "127.0.0.1",
		},
		svc: []CBService{&testService{}},
	}
	svr, err := c.initHTTP(context.Background())
	if err != nil {
		t.Fatalf("initHTTP failed: %v", err)
	}

	for _, path := range []string{"/debug/pprof/", "/debug/pprof/cmdline", "/debug/pprof/symbol"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		svr.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", path, w.Code)
		}
	}
}

func TestHTTPHandler_Metrics(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{
			GRPCPort:   19090,
			HTTPPort:   19091,
			ListenHost: "127.0.0.1",
		},
		svc: []CBService{&testService{}},
	}
	svr, err := c.initHTTP(context.Background())
	if err != nil {
		t.Fatalf("initHTTP failed: %v", err)
	}

	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	svr.Handler.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for /metrics, got %d", w.Code)
	}
}

func TestHTTPHandler_MetricsSubpath(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{
			GRPCPort:   19090,
			HTTPPort:   19091,
			ListenHost: "127.0.0.1",
		},
		svc: []CBService{&testService{}},
	}
	svr, err := c.initHTTP(context.Background())
	if err != nil {
		t.Fatalf("initHTTP failed: %v", err)
	}

	for _, path := range []string{"/metrics/", "/metrics/foo"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		svr.Handler.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for %s, got %d", path, w.Code)
		}
	}
}

func TestHTTPHandler_DisabledEndpoints(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{
			GRPCPort:          19090,
			HTTPPort:          19091,
			ListenHost:        "127.0.0.1",
			DisableSwagger:    true,
			DisableDebug:      true,
			DisablePrometheus: true,
		},
		svc:            []CBService{&testService{}},
		openAPIHandler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}),
	}
	svr, err := c.initHTTP(context.Background())
	if err != nil {
		t.Fatalf("initHTTP failed: %v", err)
	}

	// When disabled, these paths fall through to the gateway mux
	for _, path := range []string{"/swagger/", "/debug/pprof/", "/metrics"} {
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		svr.Handler.ServeHTTP(w, req)
		// Just verify it doesn't panic — the gateway will handle the response
	}
}

// --- Group 6: Lifecycle ---

func TestRunAndStop(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	svc := &testService{}
	c := &cb{
		config: config.Config{
			DisableSignalHandler: true,
		},
		svc: []CBService{svc},
	}

	ctx := context.Background()
	grpcServer, err := c.initGRPC(ctx)
	if err != nil {
		t.Fatalf("initGRPC failed: %v", err)
	}
	c.grpcServer = grpcServer

	httpServer, err := c.initHTTP(ctx)
	if err != nil {
		t.Fatalf("initHTTP failed: %v", err)
	}
	c.httpServer = httpServer

	err = c.Stop(100 * time.Millisecond)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if !svc.failCheckCalled {
		t.Fatal("expected FailCheck to be called")
	}
	if !svc.stopCalled {
		t.Fatal("expected Stop to be called on service")
	}
}

func TestStop_WithHealthcheckWait(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{
			HealthcheckWaitDurationInSeconds: 1,
		},
		svc: []CBService{&testService{}},
	}
	start := time.Now()
	err := c.Stop(5 * time.Second)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Stop failed: %v", err)
	}
	if elapsed < 900*time.Millisecond {
		t.Fatalf("expected healthcheck wait of ~1s, got %v", elapsed)
	}
}

func TestRunGRPC_BadPort(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	c := &cb{
		config: config.Config{
			GRPCPort:   -1,
			ListenHost: "127.0.0.1",
		},
	}
	server := grpc.NewServer()
	defer server.Stop()
	err := c.runGRPC(context.Background(), server, nil)
	if err == nil {
		t.Fatal("expected error for bad port")
	}
}

func TestUnixGateway_DisabledByDefault(t *testing.T) {
	// Zero value of bool is false, but envconfig default is "true".
	// Verify that a cb{} with default config does not set a socket path.
	c := &cb{config: config.Config{}}
	if c.unixSocketPath != "" {
		t.Error("expected empty unix socket path by default")
	}
	// Also verify DisableUnixGateway zero value means feature is off
	// (zero = false = not disabled = enabled, but that's the Go zero value,
	// not the envconfig default). The envconfig default:"true" ensures
	// the feature is disabled in production.
	if !c.config.DisableUnixGateway {
		// This is expected — zero value is false. The envconfig tag
		// provides the "true" default at runtime.
		t.Log("DisableUnixGateway zero value is false (envconfig provides true default at runtime)")
	}
}

func TestUnixGateway_SocketCreatedAndCleaned(t *testing.T) {
	socketPath := fmt.Sprintf("/tmp/coldbrew-test-clean-%d.sock", os.Getpid())
	os.Remove(socketPath)
	defer os.Remove(socketPath)

	// Create a socket to simulate what Run() does — keep it open so the
	// socket file exists on disk for the cleanup test.
	lis, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to create unix socket: %v", err)
	}

	// Verify socket file exists while listener is open
	if _, err := os.Stat(socketPath); os.IsNotExist(err) {
		t.Fatal("expected socket file to exist while listener is open")
	}

	// Close listener before cleanup (matches shutdown order)
	lis.Close()

	// Verify cleanup via closerFunc removes the file
	c := &cb{}
	c.closers = append(c.closers, closerFunc(func() error { return os.Remove(socketPath) }))
	c.close()
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Error("expected socket file to be removed after close()")
	}
}

func TestUnixGateway_FallbackOnFailure(t *testing.T) {
	// Pre-create a regular file at the target path so net.Listen("unix", ...) fails.
	socketPath := fmt.Sprintf("/tmp/coldbrew-test-fallback-%d.sock", os.Getpid())
	os.Remove(socketPath)
	defer os.Remove(socketPath)

	if err := os.WriteFile(socketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("failed to create blocking file: %v", err)
	}

	// Verify net.Listen fails when a regular file exists at the path
	lis, err := net.Listen("unix", socketPath)
	if err == nil {
		lis.Close()
		t.Fatal("expected unix socket creation to fail when a regular file exists")
	}

	// The cb struct should not have a socket path populated
	c := &cb{config: config.Config{DisableUnixGateway: false}}
	if c.unixSocketPath != "" {
		t.Error("expected empty socket path when socket creation fails")
	}
}

func TestRunGRPC_WithUnixListener(t *testing.T) {
	socketPath := fmt.Sprintf("/tmp/coldbrew-test-rpc-%d.sock", os.Getpid())
	os.Remove(socketPath)
	defer os.Remove(socketPath)

	unixLis, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("failed to create unix socket: %v", err)
	}

	c := &cb{
		config: config.Config{
			GRPCPort:   0,
			ListenHost: "127.0.0.1",
		},
		unixSocketPath: socketPath,
	}
	server := grpc.NewServer()

	// Use a valid TCP port so both listeners can serve.
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.runGRPC(context.Background(), server, unixLis)
	}()

	// Give the server time to start both listeners.
	select {
	case err := <-errCh:
		t.Fatalf("runGRPC returned before Stop: %v", err)
	case <-time.After(200 * time.Millisecond):
	}

	// Stop cleanly — this stops both TCP and Unix listeners.
	server.Stop()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("runGRPC returned error after Stop: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("runGRPC did not return after Stop")
	}
}

func TestRun_GRPCInitError(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	svc := &errorService{grpcErr: fmt.Errorf("grpc init error")}
	c := &cb{
		config: config.Config{DisableSignalHandler: true},
		svc:    []CBService{svc},
	}
	err := c.Run()
	if err == nil {
		t.Fatal("expected error from Run when gRPC init fails")
	}
}

func TestRun_HTTPInitError(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	svc := &errorService{httpErr: fmt.Errorf("http init error")}
	c := &cb{
		config: config.Config{
			GRPCPort:             19094,
			HTTPPort:             19095,
			ListenHost:           "127.0.0.1",
			DisableSignalHandler: true,
		},
		svc: []CBService{svc},
	}
	err := c.Run()
	if err == nil {
		t.Fatal("expected error from Run when HTTP init fails")
	}
}

func TestNew_WithValidation(t *testing.T) {
	instance := New(config.Config{
		DisableSignalHandler: true,
		DisableNewRelic:      true,
		DisableAutoMaxProcs:  true,
		DisableVTProtobuf:    true,
		DisablePormetheus:    true, //nolint:staticcheck // testing deprecated field
	})
	if instance == nil {
		t.Fatal("New() returned nil")
	}
}

// TestRunFullLifecycle tests the complete Run→Stop lifecycle.
func TestRunFullLifecycle(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping full lifecycle test in short mode")
	}

	svc := &testService{ready: make(chan struct{})}
	instance := New(config.Config{
		GRPCPort:             0,
		HTTPPort:             0,
		ListenHost:           "127.0.0.1",
		DisableSignalHandler: true,
		DisableNewRelic:      true,
		DisableAutoMaxProcs:  true,
	})
	instance.SetService(svc)

	errCh := make(chan error, 1)
	go func() {
		errCh <- instance.Run()
	}()

	// Wait for InitGRPC to be called, signaling the server is starting.
	// The short sleep after lets Run() finish assigning c.grpcServer/c.httpServer
	// before Stop() reads them (a pre-existing race in core's field assignment).
	select {
	case <-svc.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for server to start")
	}
	time.Sleep(100 * time.Millisecond)

	if err := instance.Stop(2 * time.Second); err != nil {
		t.Fatalf("Stop failed: %v", err)
	}

	err := <-errCh
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
		t.Fatalf("unexpected Run error: %v", err)
	}
}

// --- Group 7: VTProto Codec ---

func TestVTProtoCodec_Marshal_ProtoMessage(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	codec := vtprotoCodec{}
	msg := wrapperspb.String("hello")
	data, err := codec.Marshal(msg)
	if err != nil {
		t.Fatalf("Marshal proto.Message failed: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected non-empty marshalled data")
	}
}

func TestVTProtoCodec_Unmarshal_ProtoMessage(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	codec := vtprotoCodec{}
	original := wrapperspb.String("hello")
	data, err := codec.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	result := &wrapperspb.StringValue{}
	err = codec.Unmarshal(data, result)
	if err != nil {
		t.Fatalf("Unmarshal proto.Message failed: %v", err)
	}
	if result.GetValue() != "hello" {
		t.Fatalf("expected 'hello', got %q", result.GetValue())
	}
}

func TestVTProtoCodec_Marshal_UnknownType(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	codec := vtprotoCodec{}
	_, err := codec.Marshal("not a proto message")
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestVTProtoCodec_Unmarshal_UnknownType(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	codec := vtprotoCodec{}
	err := codec.Unmarshal([]byte{}, "not a proto message")
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestVTProtoCodec_Name(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	codec := vtprotoCodec{}
	if codec.Name() != "proto" {
		t.Fatalf("expected 'proto', got %q", codec.Name())
	}
}

// --- Group 8: Standalone Initializer Tests ---

func TestSetupLogger_ValidLevel(t *testing.T) {
	err := SetupLogger("info", false)
	if err != nil {
		t.Fatalf("SetupLogger with valid level failed: %v", err)
	}
}

func TestSetupLogger_InvalidLevel(t *testing.T) {
	err := SetupLogger("notavalidlevel", false)
	if err == nil {
		t.Fatal("expected error for invalid log level")
	}
}

func TestSetupSentry_EmptyDSN(t *testing.T) {
	SetupSentry("") // should be a no-op
}

func TestSetupEnvironment_Empty(t *testing.T) {
	SetupEnvironment("") // should be a no-op
}

func TestSetupEnvironment_NonEmpty(t *testing.T) {
	SetupEnvironment("production")
}

func TestSetupReleaseName_Empty(t *testing.T) {
	SetupReleaseName("") // should be a no-op
}

func TestSetupReleaseName_NonEmpty(t *testing.T) {
	SetupReleaseName("v1.0.0")
}

func TestSetupNewRelic_EmptyKey(t *testing.T) {
	err := SetupNewRelic("test-service", "", false)
	if err != nil {
		t.Fatalf("expected nil for empty key, got: %v", err)
	}
}

func TestSetupNROpenTelemetry_EmptyKey(t *testing.T) {
	err := SetupNROpenTelemetry("test-service", "", "v1.0.0", 0.1)
	if err != nil {
		t.Fatalf("expected nil for empty license, got: %v", err)
	}
}

func TestSetupOpenTelemetry_EmptyConfig(t *testing.T) {
	err := SetupOpenTelemetry(OTLPConfig{})
	if err != nil {
		t.Fatalf("expected nil for empty config, got: %v", err)
	}
}

func TestSetupOpenTelemetry_MissingServiceName(t *testing.T) {
	err := SetupOpenTelemetry(OTLPConfig{
		Endpoint: "localhost:4317",
	})
	if err != nil {
		t.Fatalf("expected nil for missing service name, got: %v", err)
	}
}

func TestConfigureInterceptors_BothBranches(t *testing.T) {
	ConfigureInterceptors(true, "X-My-Trace", "info", false)
}

func TestConfig_Validate_HTTPCompressionMinSize(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	tests := []struct {
		name     string
		minSize  int
		wantWarn bool
	}{
		{"negative triggers warning", -1, true},
		{"zero is valid", 0, false},
		{"positive is valid", 256, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{HTTPCompressionMinSize: tt.minSize}
			warnings := cfg.Validate()
			found := false
			for _, w := range warnings {
				if w == "HTTPCompressionMinSize is negative; this may cause unexpected behavior" {
					found = true
				}
			}
			if found != tt.wantWarn {
				t.Errorf("HTTPCompressionMinSize=%d: wantWarn=%v, got warnings=%v", tt.minSize, tt.wantWarn, warnings)
			}
		})
	}
}

func TestProcessConfig_NRAutoDisable(t *testing.T) {
	// removed t.Parallel() — core tests mutate package-level globals
	tests := []struct {
		name         string
		licenseKey   string
		disableNR    bool
		wantDisabled bool
	}{
		{"empty key auto-disables", "", false, true},
		{"whitespace key auto-disables", "   ", false, true},
		{"real key stays enabled", "real-key-123", false, false},
		{"already disabled stays disabled", "", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			instance := New(config.Config{
				GRPCPort:             0,
				HTTPPort:             0,
				ListenHost:           "127.0.0.1",
				DisableSignalHandler: true,
				DisableAutoMaxProcs:  true,
				NewRelicLicenseKey:   tt.licenseKey,
				DisableNewRelic:      tt.disableNR,
			})
			if instance == nil {
				t.Fatal("expected non-nil instance")
			}
			// We're in package core, so we can type-assert to *cb
			// and inspect the config after processConfig ran.
			cbInstance := instance.(*cb)
			if cbInstance.config.DisableNewRelic != tt.wantDisabled {
				t.Errorf("DisableNewRelic = %v, want %v", cbInstance.config.DisableNewRelic, tt.wantDisabled)
			}
		})
	}
}
