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

func setupLogger(logLevel string, jsonlogs bool) {
	log.SetLogger(log.NewLogger(gokit.NewLogger(loggers.WithJSONLogs(jsonlogs))))

	ll, err := loggers.ParseLevel(logLevel)
	if err != nil {
		log.Error(context.Background(), "err", "could not set log level", "level", logLevel)
	} else {
		log.SetLevel(ll)
	}
}

func setupSentry(dsn string) {
	if dsn != "" {
		notifier.InitSentry(dsn)
	}
}

func setupEnvironment(env string) {
	if env != "" {
		notifier.SetEnvironment(env)
	}
}

func setupReleaseName(rel string) {
	if rel != "" {
		notifier.SetRelease(rel)
	}
}

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

func setupNROpenTelemetry(serviceName, license, version string) {
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
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
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

func setupHystrix() {
	promC := hystrixprometheus.NewPrometheusCollector("hystrix", nil, prometheus.DefBuckets)
	metricCollector.Registry.Register(promC.Collector)
}

func configureInterceptors(DoNotLogGRPCReflection bool, traceHeaderName string) {
	if DoNotLogGRPCReflection {
		interceptors.FilterMethods = append(interceptors.FilterMethods, "grpc.reflection.v1alpha.ServerReflection")
	}
	if traceHeaderName != "" {
		notifier.SetTraceHeaderName(traceHeaderName)
	}
}

func startSignalHandler(c *cb, dur time.Duration) {
	go signalWatcher(context.Background(), c, dur)
}

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
