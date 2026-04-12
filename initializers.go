package core

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"syscall"
	"time"

	metricCollector "github.com/afex/hystrix-go/hystrix/metric_collector"
	cbotel "github.com/go-coldbrew/core/otel"
	"github.com/go-coldbrew/errors/notifier"
	"github.com/go-coldbrew/hystrixprometheus" //nolint:staticcheck // deprecated but still in use
	"github.com/go-coldbrew/interceptors"
	"github.com/go-coldbrew/log"
	"github.com/go-coldbrew/log/loggers"
	nrutil "github.com/go-coldbrew/tracing/newrelic"
	protov1 "github.com/golang/protobuf/proto" //nolint:staticcheck
	newrelic "github.com/newrelic/go-agent/v3/newrelic"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	otelmetric "go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	"go.uber.org/automaxprocs/maxprocs"
	"google.golang.org/grpc/encoding"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/protobuf/proto"
)

// SetupNewRelic sets up the New Relic tracing and monitoring agent for the service
// It uses the New Relic Go Agent to send traces to New Relic One APM and Insights
// serviceName is the name of the service
// apiKey is the New Relic license key
// tracing is a boolean to enable or disable tracing
func SetupNewRelic(serviceName, apiKey string, tracing bool) error {
	if strings.TrimSpace(apiKey) == "" {
		log.Info(context.Background(), "Not initializing NewRelic because token is empty")
		return nil
	}

	app, err := newrelic.NewApplication(
		newrelic.ConfigEnabled(true),
		newrelic.ConfigAppName(serviceName),
		newrelic.ConfigLicense(apiKey),
		newrelic.ConfigFromEnvironment(),
		newrelic.ConfigDistributedTracerEnabled(tracing),
	)
	if err != nil {
		log.Error(context.Background(), "msg", "NewRelic could not be initialized", "err", err)
		return err
	}
	nrutil.SetNewRelicApp(app)
	log.Info(context.Background(), "NewRelic initialized for "+serviceName)
	return nil
}

// SetupLogger sets up the logger using ColdBrew's slog-native Handler.
// It calls log.SetDefault which also wires slog.SetDefault, so native
// slog.LogAttrs calls automatically get ColdBrew context fields.
// logLevel is the log level to set for the logger
// jsonlogs is a boolean to enable or disable json logs
func SetupLogger(logLevel string, jsonlogs bool) error {
	ll, err := loggers.ParseLevel(logLevel)
	if err != nil {
		log.Error(context.Background(), "msg", "could not set log level", "level", logLevel, "err", err)
		return err
	}
	if log.DefaultIsSet() {
		// User already configured a custom handler via log.SetDefault — respect it.
		log.SetLevel(ll)
		return nil
	}
	log.SetDefault(log.NewHandler(loggers.WithJSONLogs(jsonlogs), loggers.WithLevel(ll)))
	return nil
}

// SetupSentry sets up the Sentry notifier
// It uses the Sentry HTTP Transport to send errors to Sentry server
// dsn is the Sentry DSN to use for sending errors
func SetupSentry(dsn string) {
	if dsn != "" {
		notifier.InitSentry(dsn)
	}
}

// SetupEnvironment sets the environment
// This is used to identify the environment in Sentry and New Relic
// env is the environment to set for the service (e.g. prod, staging, dev)
func SetupEnvironment(env string) {
	if env != "" {
		notifier.SetEnvironment(env)
	}
}

// SetupReleaseName sets the release name
// This is used to identify the release in Sentry
// rel is the release name to set for the service (e.g. v1.0.0)
func SetupReleaseName(rel string) {
	if rel != "" {
		notifier.SetRelease(rel)
	}
}

// OTLPConfig holds configuration for OpenTelemetry OTLP exporter
//
// This struct provides a flexible way to configure OpenTelemetry tracing
// with any OTLP-compatible backend (e.g., Jaeger, Honeycomb, New Relic, etc.)
type OTLPConfig struct {
	// Endpoint is the OTLP gRPC endpoint to send traces to
	// Examples: "localhost:4317", "otlp.nr-data.net:4317", "api.honeycomb.io:443"
	Endpoint string

	// Headers are custom headers to send with each request
	// Examples:
	//   New Relic: {"api-key": "your-license-key"}
	//   Honeycomb: {"x-honeycomb-team": "your-api-key"}
	Headers map[string]string

	// ServiceName is the name of the service sending traces
	ServiceName string

	// ServiceVersion is the version of the service
	ServiceVersion string

	// SamplingRatio is the ratio of traces to sample (0.0 to 1.0)
	// 1.0 means sample all traces, 0.1 means sample 10% of traces
	SamplingRatio float64

	// Compression specifies the compression type (e.g., "gzip", "none")
	// If empty, defaults to "gzip"
	Compression string

	// Insecure disables TLS verification for the connection
	// Only use this for local development or testing
	Insecure bool

	// GRPCSpanNameFormat controls gRPC span naming.
	// "short" extracts just the method name (e.g., "V0GetStats")
	// "full" keeps the full path (e.g., "/pkg.Service/V0GetStats") - default
	GRPCSpanNameFormat string

	// FilterSpanNames is a comma-separated string of span names to filter out.
	// Common use: "ServeHTTP" to filter HTTP transport spans.
	FilterSpanNames string
}

// nrOTLPEndpoint is the New Relic OTLP gRPC endpoint.
const nrOTLPEndpoint = "otlp.nr-data.net:4317"

// otelResource is the shared resource used by both TracerProvider and
// MeterProvider so that traces and metrics correlate in backends.
var otelResource *resource.Resource

// otelTracerProvider stores the concrete TracerProvider for shutdown.
var otelTracerProvider *sdktrace.TracerProvider

// otelSpanProcessor stores the custom span processor for runtime filter/transformer additions.
var otelSpanProcessor *cbotel.SpanProcessor

// buildOTELResource builds a resource with service name, version, build info,
// and VCS metadata. The result is cached in otelResource for reuse.
func buildOTELResource(serviceName, serviceVersion string) (*resource.Resource, error) {
	if otelResource != nil {
		return otelResource, nil
	}
	d := resource.Default()
	attrs := []attribute.KeyValue{
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion(serviceVersion),
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		attrs = append(attrs,
			semconv.ProcessExecutableName(filepath.Base(os.Args[0])),
			semconv.ProcessRuntimeVersion(bi.GoVersion),
		)
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				attrs = append(attrs, semconv.VCSRefHeadRevision(s.Value))
			case "vcs.time":
				attrs = append(attrs, attribute.String("vcs.time", s.Value))
			case "vcs.modified":
				attrs = append(attrs, attribute.Bool("vcs.modified", s.Value == "true"))
			}
		}
	}
	res, err := resource.New(context.Background(),
		resource.WithAttributes(attrs...),
	)
	if err != nil {
		return nil, fmt.Errorf("creating OTLP resource: %w", err)
	}
	r, err := resource.Merge(d, res)
	if err != nil {
		return nil, fmt.Errorf("merging OTLP resource: %w", err)
	}
	otelResource = r
	return r, nil
}

// SetupOpenTelemetry sets up OpenTelemetry tracing with a generic OTLP exporter.
//
// It configures a TracerProvider with the given sampling ratio and OTLP backend,
// sets it as the global provider, and stores it for graceful shutdown.
//
// Example usage with Jaeger:
//
//	config := OTLPConfig{
//	    Endpoint:       "localhost:4317",
//	    ServiceName:    "my-service",
//	    ServiceVersion: "v1.0.0",
//	    SamplingRatio:  0.1,
//	    Insecure:       true, // for local development
//	}
//	err := SetupOpenTelemetry(config)
//
// Example usage with Honeycomb:
//
//	config := OTLPConfig{
//	    Endpoint:       "api.honeycomb.io:443",
//	    Headers:        map[string]string{"x-honeycomb-team": "your-api-key"},
//	    ServiceName:    "my-service",
//	    ServiceVersion: "v1.0.0",
//	    SamplingRatio:  0.2,
//	}
//	err := SetupOpenTelemetry(config)
func SetupOpenTelemetry(config OTLPConfig) error {
	if config.ServiceName == "" || config.Endpoint == "" {
		log.Info(
			context.Background(),
			"msg",
			"not initializing opentelemetry tracing: missing serviceName or endpoint",
		)
		return nil
	}

	if config.Compression == "" {
		config.Compression = "gzip"
	}

	clientOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(config.Endpoint),
		otlptracegrpc.WithHeaders(config.Headers),
	}
	if config.Compression != "none" {
		clientOpts = append(clientOpts, otlptracegrpc.WithCompressor(config.Compression))
	}
	if config.Insecure {
		clientOpts = append(clientOpts, otlptracegrpc.WithInsecure())
	}

	otlpExporter, err := otlptrace.New(context.Background(), otlptracegrpc.NewClient(clientOpts...))
	if err != nil {
		log.Error(context.Background(), "msg", "creating OTLP trace exporter", "err", err)
		return err
	}

	r, err := buildOTELResource(config.ServiceName, config.ServiceVersion)
	if err != nil {
		log.Error(context.Background(), "msg", "building OTLP resource", "err", err)
		return err
	}

	// Default sampling ratio when not explicitly set (negative) or invalid (> 1).
	// 0 is a valid value meaning "sample nothing".
	ratio := config.SamplingRatio
	if ratio < 0 || ratio > 1 {
		ratio = 0.2
	}

	// Wrap the batcher with custom SpanProcessor for filtering/transformation.
	batcher := sdktrace.NewBatchSpanProcessor(otlpExporter)
	var filterNames []string
	if config.FilterSpanNames != "" {
		for _, name := range strings.Split(config.FilterSpanNames, ",") {
			if trimmed := strings.TrimSpace(name); trimmed != "" {
				filterNames = append(filterNames, trimmed)
			}
		}
	}
	processor := cbotel.NewSpanProcessor(batcher, cbotel.SpanProcessorConfig{
		GRPCSpanNameFormat: config.GRPCSpanNameFormat,
		FilterSpanNames:    filterNames,
	})
	otelSpanProcessor = processor

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithSpanProcessor(processor),
		sdktrace.WithResource(r),
	)
	otelTracerProvider = tracerProvider

	// Set global propagator for W3C trace context + baggage propagation.
	// This is required for linking spans across HTTP→gRPC boundaries.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	otel.SetTracerProvider(tracerProvider)

	log.Info(context.Background(), "msg", "Initialized opentelemetry tracing", "endpoint", config.Endpoint)
	return nil
}

// SetupOTELMetrics creates a MeterProvider with an OTLP gRPC exporter that
// reuses the same resource as the TracerProvider (set by SetupOpenTelemetry).
// The MeterProvider is set as the global OTel MeterProvider.
//
// Call this after SetupOpenTelemetry so the shared resource is available.
func SetupOTELMetrics(config OTLPConfig, interval time.Duration) (*sdkmetric.MeterProvider, error) {
	if config.Endpoint == "" {
		return nil, fmt.Errorf("OTLP endpoint is required for OTEL metrics")
	}
	if interval <= 0 {
		return nil, fmt.Errorf("OTEL metrics interval must be positive, got %v", interval)
	}
	if config.Compression == "" {
		config.Compression = "gzip"
	}

	exporterOpts := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(config.Endpoint),
		otlpmetricgrpc.WithHeaders(config.Headers),
	}
	if config.Compression != "none" {
		exporterOpts = append(exporterOpts, otlpmetricgrpc.WithCompressor(config.Compression))
	}
	if config.Insecure {
		exporterOpts = append(exporterOpts, otlpmetricgrpc.WithInsecure())
	}

	exporter, err := otlpmetricgrpc.New(context.Background(), exporterOpts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create OTLP metric exporter: %w", err)
	}

	r := otelResource
	if r == nil {
		// Fallback: build resource if SetupOpenTelemetry wasn't called first.
		if config.ServiceName == "" {
			return nil, fmt.Errorf("OTEL service name is required when tracing resource is not initialized")
		}
		r, err = buildOTELResource(config.ServiceName, config.ServiceVersion)
		if err != nil {
			return nil, fmt.Errorf("building OTLP resource for metrics: %w", err)
		}
	}

	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(r),
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exporter,
			sdkmetric.WithInterval(interval),
		)),
	)
	otel.SetMeterProvider(mp)
	return mp, nil
}

// OTELMeterProvider returns the global OTel MeterProvider. This is a convenience
// accessor for code that needs the interface type.
func OTELMeterProvider() otelmetric.MeterProvider {
	return otel.GetMeterProvider()
}

// SetupNROpenTelemetry sets up OpenTelemetry tracing with New Relic
//
// This function configures OpenTelemetry to send traces to New Relic's OTLP endpoint.
// It's a convenience wrapper around SetupOpenTelemetry with New Relic-specific configuration.
//
// Parameters:
//   - serviceName: the name of the service
//   - license: the New Relic license key
//   - version: the version of the service
//   - ratio: the sampling ratio to use for traces (0.0 to 1.0)
func SetupNROpenTelemetry(serviceName, license, version string, ratio float64) error {
	if strings.TrimSpace(license) == "" {
		log.Info(context.Background(), "msg", "not initializing opentelemetry (nr): missing license key")
		return nil
	}
	// Use the generic SetupOpenTelemetry with New Relic specific configuration
	config := OTLPConfig{
		Endpoint:       nrOTLPEndpoint,
		Headers:        map[string]string{"api-key": license},
		ServiceName:    serviceName,
		ServiceVersion: version,
		SamplingRatio:  ratio,
		Compression:    "gzip",
	}
	return SetupOpenTelemetry(config)
}

var hystrixOnce sync.Once

// SetupHystrixPrometheus sets up the hystrix metrics
// This is a workaround for hystrix-go not supporting the prometheus registry
// It uses sync.Once to ensure the Prometheus collectors are only registered once,
// since duplicate registration panics.
func SetupHystrixPrometheus() {
	hystrixOnce.Do(func() {
		promC := hystrixprometheus.NewPrometheusCollector("hystrix", nil, prometheus.DefBuckets)
		metricCollector.Registry.Register(promC.Collector)
	})
}

// configureInterceptors configures the interceptors package with the provided settings.
func configureInterceptors(DoNotLogGRPCReflection bool, traceHeaderName string, responseTimeLogLevel string, responseTimeLogErrorOnly bool, defaultTimeoutInSeconds int) {
	if DoNotLogGRPCReflection {
		methods := append(interceptors.FilterMethods, "grpc.reflection.v1alpha.ServerReflection") //nolint:staticcheck // FilterMethods read is fine, using SetFilterMethods to write
		interceptors.SetFilterMethods(context.Background(), methods)
	}
	if traceHeaderName != "" {
		notifier.SetTraceHeaderName(traceHeaderName)
	}
	if responseTimeLogLevel != "" {
		level, err := loggers.ParseLevel(responseTimeLogLevel)
		if err != nil {
			log.Warn(context.Background(), "msg", "invalid RESPONSE_TIME_LOG_LEVEL, defaulting to info", "value", responseTimeLogLevel, "err", err)
			level = loggers.InfoLevel
		}
		interceptors.SetResponseTimeLogLevel(context.Background(), level)
	}
	interceptors.SetResponseTimeLogErrorOnly(responseTimeLogErrorOnly)
	if defaultTimeoutInSeconds > 0 {
		interceptors.SetDefaultTimeout(time.Second * time.Duration(defaultTimeoutInSeconds))
	} else {
		interceptors.SetDefaultTimeout(0)
	}
}

// SetupAutoMaxProcs sets up the GOMAXPROCS to match Linux container CPU quota
// This is used to set the GOMAXPROCS to the number of CPUs allocated to the container
func SetupAutoMaxProcs() {
	// Automatically set GOMAXPROCS to match Linux container CPU quota
	// https://kubernetes.io/docs/tasks/configure-pod-container/assign-cpu-resource/
	logger := func(format string, v ...interface{}) {
		log.Info(context.Background(), "automaxprocs", fmt.Sprintf(format, v...))
	}
	_, err := maxprocs.Set(maxprocs.Logger(logger))
	if err != nil {
		log.Error(context.Background(), "msg", "automaxprocs", "err", err)
	}
}

// startSignalHandler starts a goroutine that listens for SIGTERM and SIGINT
func startSignalHandler(c *cb, dur time.Duration) {
	go signalWatcher(context.Background(), c, dur)
}

// signalWatcher is a goroutine that listens for SIGTERM and SIGINT signals
// and calls Stop on the provided cb with the provided duration.
func signalWatcher(ctx context.Context, c *cb, dur time.Duration) {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGTERM, syscall.SIGINT)
	log.Info(ctx, "signal watcher started")
	for sig := range signals {
		log.Info(ctx, "signal: shutdown on "+sig.String())
		err := c.Stop(dur)
		log.Info(ctx, "signal: shutdown completed "+sig.String(), "err", err)
		break
	}
}

// InitializeVTProto initializes the vtproto package for use with the service
//
// https://github.com/planetscale/vtprotobuf?tab=readme-ov-file#mixing-protobuf-implementations-with-grpc
func InitializeVTProto() {
	encoding.RegisterCodec(vtprotoCodec{})
}

type vtprotoCodec struct{}

type vtprotoMessage interface {
	MarshalVT() ([]byte, error)
	UnmarshalVT([]byte) error
}

func (vtprotoCodec) Marshal(v any) (data []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error(context.Background(), "msg", "failed to marshal", "err", r)
			err = fmt.Errorf("failed to marshal, err: %v", r)
			notifier.NotifyOnPanic(err, r)
		}
	}()
	switch v := v.(type) {
	case vtprotoMessage:
		data, err = v.MarshalVT()
	case proto.Message:
		data, err = proto.Marshal(v)
	case protov1.Message:
		data, err = proto.Marshal(protov1.MessageV2(v))
	default:
		return nil, fmt.Errorf("failed to marshal, message is %T, must satisfy the vtprotoMessage interface or want proto.Message", v)
	}
	return
}

func (vtprotoCodec) Unmarshal(data []byte, v any) (err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Error(context.Background(), "msg", "failed to marshal", "err", r)
			err = fmt.Errorf("failed to unmarshal, err: %v", r)
			notifier.NotifyOnPanic(err, r)
		}
	}()
	switch v := v.(type) {
	case vtprotoMessage:
		err = v.UnmarshalVT(data)
	case proto.Message:
		err = proto.Unmarshal(data, v)
	case protov1.Message:
		err = protov1.Unmarshal(data, v)
	default:
		err = fmt.Errorf("failed to unmarshal, message is %T, must satisfy the vtprotoMessage interface or want proto.Message", v)
	}
	return
}

func (vtprotoCodec) Name() string {
	// name registered for the proto compressor
	return "proto"
}

// AddOTELSpanFilter adds a custom span filter at runtime.
// Filters are checked for each span; if any filter returns true, the span is dropped.
// Must be called after SetupOpenTelemetry; no-op if OTEL is not initialized.
func AddOTELSpanFilter(f cbotel.SpanFilter) {
	if otelSpanProcessor != nil {
		otelSpanProcessor.AddFilter(f)
	}
}

// AddOTELSpanTransformer adds a custom span transformer at runtime.
// Transformers are applied in order; first non-empty result wins.
// Must be called after SetupOpenTelemetry; no-op if OTEL is not initialized.
func AddOTELSpanTransformer(t cbotel.SpanTransformer) {
	if otelSpanProcessor != nil {
		otelSpanProcessor.AddTransformer(t)
	}
}
