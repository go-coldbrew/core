package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-coldbrew/core/config"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
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
}

func (s *testService) InitHTTP(_ context.Context, _ *runtime.ServeMux, _ string, _ []grpc.DialOption) error {
	s.initHTTPCalled = true
	return nil
}

func (s *testService) InitGRPC(_ context.Context, _ *grpc.Server) error {
	s.initGRPCCalled = true
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

// closerFunc adapts a function to io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }

// --- Group 1: Utility Functions ---

func TestSetOpenAPIHandler(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	timedCall(ctx, func() {
		time.Sleep(5 * time.Second)
	})
	// Should return without hanging
}

func TestClose_WithClosers(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	c := &cb{
		closers: []io.Closer{nil},
	}
	c.close() // should skip nil without panic
}

func TestLoadTLSCredentials_BadFiles(t *testing.T) {
	t.Parallel()
	_, err := loadTLSCredentials("/nonexistent/cert.pem", "/nonexistent/key.pem", false)
	if err == nil {
		t.Fatal("expected error for nonexistent TLS files")
	}
}

func TestTracingWrapper(t *testing.T) {
	t.Parallel()
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

func TestGetCustomHeaderMatcher_EmptyPrefixes(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	matcher := getCustomHeaderMatcher([]string{""}, "X-Trace-Id")
	_, matched := matcher("X-Random-Header")
	if matched {
		t.Fatal("empty prefix should not match anything")
	}
}

// --- Group 2: gRPC Server Options ---

func TestGetGRPCServerOptions_Default(t *testing.T) {
	t.Parallel()
	c := &cb{config: config.Config{}}
	opts := c.getGRPCServerOptions()
	if len(opts) < 2 {
		t.Fatalf("expected at least 2 server options, got %d", len(opts))
	}
}

func TestGetGRPCServerOptions_WithMsgSizeLimits(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
		ShutdownDurationInSeconds: 5,
	}}
	c.processConfig()
}

// --- Group 4: Init Server Functions ---

func TestInitGRPC(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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

func TestHTTPHandler_DisabledEndpoints(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	c := &cb{
		config: config.Config{
			GRPCPort:   -1,
			ListenHost: "127.0.0.1",
		},
	}
	server := grpc.NewServer()
	defer server.Stop()
	err := c.runGRPC(context.Background(), server)
	if err == nil {
		t.Fatal("expected error for bad port")
	}
}

func TestRun_GRPCInitError(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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

	svc := &testService{}
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

	time.Sleep(300 * time.Millisecond)

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
	t.Parallel()
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
	t.Parallel()
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
	t.Parallel()
	codec := vtprotoCodec{}
	_, err := codec.Marshal("not a proto message")
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestVTProtoCodec_Unmarshal_UnknownType(t *testing.T) {
	t.Parallel()
	codec := vtprotoCodec{}
	err := codec.Unmarshal([]byte{}, "not a proto message")
	if err == nil {
		t.Fatal("expected error for unknown type")
	}
}

func TestVTProtoCodec_Name(t *testing.T) {
	t.Parallel()
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
	ConfigureInterceptors(true, "X-My-Trace")
}
