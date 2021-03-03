package core

import (
	"context"
	"io"
	"strings"

	metricCollector "github.com/afex/hystrix-go/hystrix/metric_collector"
	"github.com/go-coldbrew/errors/notifier"
	"github.com/go-coldbrew/hystrixprometheus"
	"github.com/go-coldbrew/log"
	nrutil "github.com/go-coldbrew/tracing/newrelic"
	jprom "github.com/jaegertracing/jaeger-lib/metrics/prometheus"
	newrelic "github.com/newrelic/go-agent/v3/newrelic"
	"github.com/opentracing/opentracing-go"
	"github.com/prometheus/client_golang/prometheus"
	jaegerconfig "github.com/uber/jaeger-client-go/config"
	"github.com/uber/jaeger-client-go/zipkin"
)

func setupNewRelic(serviceName, apiKey string) {
	if strings.TrimSpace(apiKey) == "" {
		log.Info(context.Background(), "Not initializing NewRelic because token is empty")
		return
	}

	app, err := newrelic.NewApplication(
		newrelic.ConfigEnabled(true),
		newrelic.ConfigAppName(serviceName),
		newrelic.ConfigLicense(apiKey),
	)
	if err != nil {
		log.Error(context.Background(), "msg", "NewRelic could not be initialized", "err", err)
		return
	}
	nrutil.SetNewRelicApp(app)
	log.Info(context.Background(), "NewRelic initialized for "+serviceName)
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
	return closer
}

func setupHystrix() {
	promC := hystrixprometheus.NewPrometheusCollector("hystrix", nil, prometheus.DefBuckets)
	metricCollector.Registry.Register(promC.Collector)
}
