package core

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	metricCollector "github.com/afex/hystrix-go/hystrix/metric_collector"
	"github.com/go-coldbrew/errors/notifier"
	"github.com/go-coldbrew/hystrixprometheus"
	"github.com/go-coldbrew/interceptors"
	"github.com/go-coldbrew/log"
	"github.com/go-coldbrew/log/loggers"
	"github.com/go-coldbrew/log/loggers/gokit"
	nrutil "github.com/go-coldbrew/tracing/newrelic"
	protov1 "github.com/golang/protobuf/proto" //nolint:staticcheck
	jprom "github.com/jaegertracing/jaeger-lib/metrics/prometheus"
	newrelic "github.com/newrelic/go-agent/v3/newrelic"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	jaegerconfig "github.com/uber/jaeger-client-go/config"
	"github.com/uber/jaeger-client-go/zipkin"
	"go.opentelemetry.io/otel"
	otelBridge "go.opentelemetry.io/otel/bridge/opentracing"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.12.0"
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

// SetupLogger sets up the logger
// It uses the coldbrew logger to log messages to stdout
// logLevel is the log level to set for the logger
// jsonlogs is a boolean to enable or disable json logs
func SetupLogger(logLevel string, jsonlogs bool) error {
	log.SetLogger(log.NewLogger(gokit.NewLogger(loggers.WithJSONLogs(jsonlogs))))

	ll, err := loggers.ParseLevel(logLevel)
	if err != nil {
		log.Error(context.Background(), "err", "could not set log level", "level", logLevel)
		return err
	}
	log.SetLevel(ll)
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

// setupJaeger sets up the Jaeger tracing
// It uses the Jaeger Zipkin B3 HTTP Propagator to propagate the tracing headers to downstream services
func setupJaeger(serviceName string) io.Closer {
	conf, err := jaegerconfig.FromEnv()
	if err != nil {
		log.Info(context.Background(), "msg", "could not initialize jaeger", "err", err)
		return nil
	}
	conf.ServiceName = serviceName
	zipkinPropagator := zipkin.NewZipkinB3HTTPHeaderPropagator()
	jaegerTracer, closer, err := conf.NewTracer(
		jaegerconfig.Injector(opentracing.HTTPHeaders, zipkinPropagator),
		jaegerconfig.Extractor(opentracing.HTTPHeaders, zipkinPropagator),
		jaegerconfig.ZipkinSharedRPCSpan(true),
		jaegerconfig.Metrics(jprom.New()),
	)
	if err != nil {
		log.Info(context.Background(), "msg", "could not initialize jaeger", "err", err)
		return nil
	}
	opentracing.SetGlobalTracer(jaegerTracer)
	log.Info(context.Background(), "msg", "jaeger tracing initialized")
	return closer
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

	// UseOpenTracingBridge determines whether to set up OpenTracing compatibility bridge
	// This allows using OpenTracing instrumentation with OpenTelemetry
	UseOpenTracingBridge bool

	// Insecure disables TLS verification for the connection
	// Only use this for local development or testing
	Insecure bool
}

// SetupOpenTelemetry sets up OpenTelemetry tracing with a generic OTLP exporter
//
// This function provides a flexible way to configure OpenTelemetry tracing
// with any OTLP-compatible backend. It sets up the trace provider, configures
// sampling, and optionally sets up an OpenTracing bridge for compatibility.
//
// Example usage with Jaeger:
//
//	config := OTLPConfig{
//	    Endpoint:             "localhost:4317",
//	    ServiceName:          "my-service",
//	    ServiceVersion:       "v1.0.0",
//	    SamplingRatio:        0.1,
//	    UseOpenTracingBridge: true,
//	    Insecure:            true,  // for local development
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

	// Default compression to gzip if not specified
	if config.Compression == "" {
		config.Compression = "gzip"
	}

	// Build client options
	clientOpts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(config.Endpoint),
		otlptracegrpc.WithHeaders(config.Headers),
	}

	// Add compression if specified
	if config.Compression != "none" {
		clientOpts = append(clientOpts, otlptracegrpc.WithCompressor(config.Compression))
	}

	// Add insecure option if needed
	if config.Insecure {
		clientOpts = append(clientOpts, otlptracegrpc.WithInsecure())
	}

	otlpExporter, err := otlptrace.New(context.Background(), otlptracegrpc.NewClient(clientOpts...))
	if err != nil {
		log.Error(context.Background(), "msg", "creating OTLP trace exporter", "err", err)
		return err
	}

	d := resource.Default()
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			// the service name used to display traces in backends
			semconv.ServiceNameKey.String(config.ServiceName),
			semconv.ServiceVersionKey.String(config.ServiceVersion),
		),
	)
	if err != nil {
		log.Error(context.Background(), "msg", "creating OTLP resource", "err", err)
		return err
	}
	r, err := resource.Merge(d, res)
	if err != nil {
		log.Error(context.Background(), "msg", "merging OTLP resource", "err", err)
		return err
	}
	// Clamp/Default sampling ratio
	ratio := config.SamplingRatio
	if ratio <= 0 || ratio > 1 {
		ratio = 0.2
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))),
		sdktrace.WithBatcher(otlpExporter),
		sdktrace.WithResource(r),
	)

	if config.UseOpenTracingBridge {
		otelTracer := tracerProvider.Tracer(config.ServiceName)
		// Use the bridgeTracer as your OpenTracing tracer.
		bridgeTracer, wrapperTracerProvider := otelBridge.NewTracerPair(otelTracer)

		otel.SetTracerProvider(wrapperTracerProvider)
		opentracing.SetGlobalTracer(bridgeTracer)
	} else {
		otel.SetTracerProvider(tracerProvider)
	}

	log.Info(context.Background(), "msg", "Initialized opentelemetry tracing", "endpoint", config.Endpoint)
	return nil
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
		Endpoint:             "otlp.nr-data.net:4317",
		Headers:              map[string]string{"api-key": license},
		ServiceName:          serviceName,
		ServiceVersion:       version,
		SamplingRatio:        ratio,
		Compression:          "gzip",
		UseOpenTracingBridge: true,
	}
	return SetupOpenTelemetry(config)
}

// SetupHystrixPrometheus sets up the hystrix metrics
// This is a workaround for hystrix-go not supporting the prometheus registry
func SetupHystrixPrometheus() {
	promC := hystrixprometheus.NewPrometheusCollector("hystrix", nil, prometheus.DefBuckets)
	metricCollector.Registry.Register(promC.Collector)
}

// ConfigureInterceptors configures the interceptors package with the provided
// DoNotLogGRPCReflection is a boolean that indicates whether to log the grpc.reflection.v1alpha.ServerReflection service calls in logs
// traceHeaderName is the name of the header to use for tracing (e.g. X-Trace-Id) - if empty, defaults to X-Trace-Id
func ConfigureInterceptors(DoNotLogGRPCReflection bool, traceHeaderName string) {
	if DoNotLogGRPCReflection {
		interceptors.FilterMethods = append(
			interceptors.FilterMethods,
			"grpc.reflection.v1alpha.ServerReflection",
		)
	}
	if traceHeaderName != "" {
		notifier.SetTraceHeaderName(traceHeaderName)
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
