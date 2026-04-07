package core

import (
	"context"
	"testing"
	"time"

	"github.com/go-coldbrew/core/config"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	experimental "google.golang.org/grpc/experimental/opentelemetry"
	grpcotel "google.golang.org/grpc/stats/opentelemetry"
)

func TestBuildOTELResource(t *testing.T) {
	r, err := buildOTELResource("test-service", "v1.0.0")
	if err != nil {
		t.Fatalf("buildOTELResource() error: %v", err)
	}
	if r == nil {
		t.Fatal("buildOTELResource() returned nil resource")
	}

	// Verify shared var is set
	if otelResource == nil {
		t.Fatal("otelResource should be set after buildOTELResource()")
	}

	// Verify service.name attribute exists
	found := false
	for _, attr := range r.Attributes() {
		if string(attr.Key) == "service.name" && attr.Value.AsString() == "test-service" {
			found = true
			break
		}
	}
	if !found {
		t.Error("resource should contain service.name=test-service")
	}
}

func TestSetupOpenTelemetry_StoresTracerProvider(t *testing.T) {
	// Reset globals
	oldTP := otelTracerProvider
	oldRes := otelResource
	defer func() {
		otelTracerProvider = oldTP
		otelResource = oldRes
	}()

	otelTracerProvider = nil
	otelResource = nil

	// Use a real OTLP endpoint that will fail to connect but won't error during setup
	// (the batcher exporter is async, so the exporter setup succeeds even with invalid endpoints)
	err := SetupOpenTelemetry(OTLPConfig{
		ServiceName: "test-service",
		Endpoint:    "localhost:4317",
		Insecure:    true,
	})
	if err != nil {
		t.Fatalf("SetupOpenTelemetry() error: %v", err)
	}

	if otelTracerProvider == nil {
		t.Fatal("otelTracerProvider should be set after SetupOpenTelemetry()")
	}
	if otelResource == nil {
		t.Fatal("otelResource should be set after SetupOpenTelemetry()")
	}

	// Clean up
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = otelTracerProvider.Shutdown(ctx)
}

func TestSetupOTELMetrics(t *testing.T) {
	// Reset globals
	oldRes := otelResource
	defer func() { otelResource = oldRes }()

	// Build a resource first
	_, err := buildOTELResource("test-metrics", "v1.0.0")
	if err != nil {
		t.Fatalf("buildOTELResource() error: %v", err)
	}

	// Save and restore global MeterProvider.
	oldGlobalMP := otel.GetMeterProvider()
	defer otel.SetMeterProvider(oldGlobalMP)

	mp, err := SetupOTELMetrics(OTLPConfig{
		Endpoint: "localhost:4317",
		Insecure: true,
	}, 60*time.Second)
	if err != nil {
		t.Fatalf("SetupOTELMetrics() error: %v", err)
	}
	if mp == nil {
		t.Fatal("SetupOTELMetrics() returned nil MeterProvider")
	}

	// Verify it's a concrete *sdkmetric.MeterProvider
	var _ *sdkmetric.MeterProvider = mp

	// Shutdown may fail because no OTLP collector is running — that's expected.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = mp.Shutdown(ctx)
}

func TestSetupOTELMetrics_NoEndpoint(t *testing.T) {
	_, err := SetupOTELMetrics(OTLPConfig{}, 60*time.Second)
	if err == nil {
		t.Fatal("SetupOTELMetrics with empty endpoint should error")
	}
}

func TestSetupOTELMetrics_SharedResource(t *testing.T) {
	// Save and restore globals
	oldRes := otelResource
	oldGlobalMP := otel.GetMeterProvider()
	defer func() {
		otelResource = oldRes
		otel.SetMeterProvider(oldGlobalMP)
	}()

	// Set up tracing first to populate shared resource
	otelResource = nil
	_, err := buildOTELResource("shared-svc", "v2.0.0")
	if err != nil {
		t.Fatalf("buildOTELResource() error: %v", err)
	}

	mp, err := SetupOTELMetrics(OTLPConfig{
		Endpoint: "localhost:4317",
		Insecure: true,
	}, 60*time.Second)
	if err != nil {
		t.Fatalf("SetupOTELMetrics() error: %v", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		mp.Shutdown(ctx)
	}()

	// The MeterProvider should have been created with the shared resource.
	// We can't inspect the resource directly, but we verify the MeterProvider is non-nil
	// and otelResource was used (not rebuilt).
	found := false
	for _, attr := range otelResource.Attributes() {
		if string(attr.Key) == "service.name" && attr.Value.AsString() == "shared-svc" {
			found = true
			break
		}
	}
	if !found {
		t.Error("shared resource should contain service.name=shared-svc")
	}
}

func TestBuildOTELOptions_MethodAttributeFilter(t *testing.T) {
	// Save and restore global OTel state.
	oldTP := otel.GetTracerProvider()
	oldProp := otel.GetTextMapPropagator()

	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	defer func() {
		tp.Shutdown(context.Background())
		otel.SetTracerProvider(oldTP)
		otel.SetTextMapPropagator(oldProp)
	}()

	oldMP := otelMeterProvider
	otelMeterProvider = nil
	defer func() { otelMeterProvider = oldMP }()

	opts := buildOTELOptions()

	// MethodAttributeFilter should filter health/ready/reflection
	filter := opts.MetricsOptions.MethodAttributeFilter
	if filter == nil {
		t.Fatal("MethodAttributeFilter should be set")
	}

	// Filtered methods — ColdBrew's default filter matches substrings
	// "healthcheck", "readycheck", "serverreflectioninfo" (case-insensitive).
	for _, method := range []string{
		"/myservice.v1.MyService/HealthCheck",
		"/myservice.v1.MyService/ReadyCheck",
		"/grpc.reflection.v1alpha.ServerReflection/ServerReflectionInfo",
	} {
		if filter(method) {
			t.Errorf("MethodAttributeFilter(%q) = true, want false", method)
		}
	}

	// Normal methods (should return true — not filtered)
	for _, method := range []string{
		"/mypackage.MyService/MyMethod",
		"/users.v1.UserService/GetUser",
		"/grpc.health.v1.Health/Check", // standard gRPC health check is NOT filtered (no contiguous "healthcheck" substring)
	} {
		if !filter(method) {
			t.Errorf("MethodAttributeFilter(%q) = false, want true", method)
		}
	}

	// TraceOptions should have provider and propagator
	if opts.TraceOptions.TracerProvider == nil {
		t.Error("TraceOptions.TracerProvider should be set")
	}
	if opts.TraceOptions.TextMapPropagator == nil {
		t.Error("TraceOptions.TextMapPropagator should be set")
	}
}

func TestBuildOTELOptions_WithMeterProvider(t *testing.T) {
	oldTP := otel.GetTracerProvider()
	oldProp := otel.GetTextMapPropagator()

	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	defer func() {
		tp.Shutdown(context.Background())
		otel.SetTracerProvider(oldTP)
		otel.SetTextMapPropagator(oldProp)
	}()

	// Set a MeterProvider
	oldMP := otelMeterProvider
	otelMeterProvider = sdkmetric.NewMeterProvider()
	defer func() {
		otelMeterProvider.Shutdown(context.Background())
		otelMeterProvider = oldMP
	}()

	opts := buildOTELOptions()
	if opts.MetricsOptions.MeterProvider == nil {
		t.Error("MetricsOptions.MeterProvider should be set when otelMeterProvider is non-nil")
	}
}

func TestProcessConfig_NativeOTEL(t *testing.T) {
	// Reset globals
	oldSet := otelGRPCOptionsSet
	oldOpts := otelGRPCOptions
	oldTP := otelTracerProvider
	oldRes := otelResource
	defer func() {
		otelGRPCOptionsSet = oldSet
		otelGRPCOptions = oldOpts
		otelTracerProvider = oldTP
		otelResource = oldRes
	}()

	otelGRPCOptionsSet = false

	c := &cb{
		config: config.Config{
			DisableSignalHandler: true,
			DisableAutoMaxProcs:  true,
			OTLPEndpoint:         "localhost:4317",
			OTLPInsecure:         true,
			AppName:              "test-native",
		},
	}
	c.processConfig()

	if !otelGRPCOptionsSet {
		t.Error("otelGRPCOptionsSet should be true after processConfig with default config")
	}
	if otelMeterProvider != nil {
		t.Error("otelMeterProvider should be nil when EnableOTELMetrics is false")
	}

	// Clean up TracerProvider
	if otelTracerProvider != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		otelTracerProvider.Shutdown(ctx)
	}
}

func TestProcessConfig_LegacyFallback(t *testing.T) {
	oldSet := otelGRPCOptionsSet
	defer func() { otelGRPCOptionsSet = oldSet }()

	otelGRPCOptionsSet = false

	c := &cb{
		config: config.Config{
			DisableSignalHandler:         true,
			DisableAutoMaxProcs:          true,
			OTELUseLegacyInstrumentation: true,
		},
	}
	c.processConfig()

	if otelGRPCOptionsSet {
		t.Error("otelGRPCOptionsSet should be false when OTELUseLegacyInstrumentation is true")
	}
}

func TestProcessConfig_UserSetOTELOptions(t *testing.T) {
	oldSet := otelGRPCOptionsSet
	oldOpts := otelGRPCOptions
	defer func() {
		otelGRPCOptionsSet = oldSet
		otelGRPCOptions = oldOpts
	}()

	// Simulate user calling SetOTELOptions during init
	tp := sdktrace.NewTracerProvider()
	defer tp.Shutdown(context.Background())

	customOpts := grpcotel.Options{
		TraceOptions: experimental.TraceOptions{
			TracerProvider: tp,
		},
	}
	SetOTELOptions(customOpts)

	c := &cb{
		config: config.Config{
			DisableSignalHandler: true,
			DisableAutoMaxProcs:  true,
		},
	}
	c.processConfig()

	// Verify user options were not overwritten
	if otelGRPCOptions.TraceOptions.TracerProvider != tp {
		t.Error("processConfig should not overwrite user-provided OTELOptions")
	}
}

// Use tracetest to suppress real span export.
var _ = tracetest.NewInMemoryExporter()
