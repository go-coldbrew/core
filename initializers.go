package core

import (
	"context"
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
)

// setupNewRelic sets up the New Relic tracing and monitoring agent for the service
// It uses the New Relic Go Agent to send traces to New Relic One APM and Insights
func setupNewRelic(serviceName, apiKey string, tracing bool) {
	if strings.TrimSpace(apiKey) == "" {
		log.Info(context.Background(), "Not initializing NewRelic because token is empty")
		return
	}

	app, err := newrelic.NewApplication(
		newrelic.ConfigEnabled(true),
		newrelic.ConfigAppName(serviceName),
		newrelic.ConfigLicense(apiKey),
		newrelic.ConfigFromEnvironment(),
	)
	if err != nil {
		log.Error(context.Background(), "msg", "NewRelic could not be initialized", "err", err)
		return
	}
	nrutil.SetNewRelicApp(app)
	log.Info(context.Background(), "NewRelic initialized for "+serviceName)
}

// setupLogger sets up the logger
// It uses the coldbrew logger to log messages to stdout
func setupLogger(logLevel string, jsonlogs bool) {
	log.SetLogger(log.NewLogger(gokit.NewLogger(loggers.WithJSONLogs(jsonlogs))))

	ll, err := loggers.ParseLevel(logLevel)
	if err != nil {
		log.Error(context.Background(), "err", "could not set log level", "level", logLevel)
	} else {
		log.SetLevel(ll)
	}
}

// setupSentry sets up the Sentry notifier
// It uses the Sentry HTTP Transport to send errors to Sentry server
func setupSentry(dsn string) {
	if dsn != "" {
		notifier.InitSentry(dsn)
	}
}

// setupEnvironment sets the environment
func setupEnvironment(env string) {
	if env != "" {
		notifier.SetEnvironment(env)
	}
}

// setupReleaseName sets the release name
// This is used to identify the release in Sentry
func setupReleaseName(rel string) {
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

// setupOpenTelemetry sets up the OpenTelemetry tracing
// It uses the New Relic OTLP exporter to send traces to New Relic One APM and Insights
func setupNROpenTelemetry(serviceName, license, version string, ratio float64) {
	if serviceName == "" || license == "" {
		log.Info(context.Background(), "msg", "not initializing NR opentelemetry tracing")
		return
	}
	var headers = map[string]string{
		"api-key": license,
	}

	var clientOpts = []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint("otlp.nr-data.net:4317"),
		otlptracegrpc.WithHeaders(headers),
		otlptracegrpc.WithCompressor("gzip"),
	}

	otlpExporter, err := otlptrace.New(context.Background(), otlptracegrpc.NewClient(clientOpts...))
	if err != nil {
		log.Error(context.Background(), "msg", "creating OTLP trace exporter", "err", err)
		return
	}

	d := resource.Default()
	res, err := resource.New(context.Background(),
		resource.WithAttributes(
			// the service name used to display traces in backends
			semconv.ServiceNameKey.String(serviceName),
			semconv.ServiceVersionKey.String(version),
		),
	)
	if err != nil {
		log.Error(context.Background(), "msg", "creating OTLP resource", "err", err)
		return
	}
	r, err := resource.Merge(d, res)

	if err != nil {
		log.Error(context.Background(), "msg", "merging OTLP resource", "err", err)
		return
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))), // sample 20%
		sdktrace.WithBatcher(otlpExporter),
		sdktrace.WithResource(r),
	)
	otelTracer := tracerProvider.Tracer("")
	// Use the bridgeTracer as your OpenTracing tracer.
	bridgeTracer, wrapperTracerProvider := otelBridge.NewTracerPair(otelTracer)

	otel.SetTracerProvider(wrapperTracerProvider)
	opentracing.SetGlobalTracer(bridgeTracer)
	log.Info(context.Background(), "msg", "Initialized NR opentelemetry tracing")
}

// setupHystrix sets up the hystrix metrics
// This is a workaround for hystrix-go not supporting the prometheus registry
func setupHystrix() {
	promC := hystrixprometheus.NewPrometheusCollector("hystrix", nil, prometheus.DefBuckets)
	metricCollector.Registry.Register(promC.Collector)
}

// configureInterceptors configures the interceptors package with the provided
func configureInterceptors(DoNotLogGRPCReflection bool, traceHeaderName string) {
	if DoNotLogGRPCReflection {
		interceptors.FilterMethods = append(interceptors.FilterMethods, "grpc.reflection.v1alpha.ServerReflection")
	}
	if traceHeaderName != "" {
		notifier.SetTraceHeaderName(traceHeaderName)
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
		c.Stop(dur)
		log.Info(ctx, "signal: shutdown completed "+sig.String())
		break
	}
}
