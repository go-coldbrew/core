package core

import (
	"context"
	"strings"

	metricCollector "github.com/afex/hystrix-go/hystrix/metric_collector"
	"github.com/go-coldbrew/errors/notifier"
	"github.com/go-coldbrew/hystrixprometheus"
	"github.com/go-coldbrew/log"
	nrutil "github.com/go-coldbrew/tracing/newrelic"
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
		log.Error(context.Background(), err)
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

func setupJaeger(serviceName, localAgentHostPort, collectorEndpoint, samplerType string, samplerParam float64) {
	if localAgentHostPort == "" && collectorEndpoint == "" {
		// do not init
		return
	}
	conf := jaegerconfig.Configuration{
		Reporter: &jaegerconfig.ReporterConfig{
			LocalAgentHostPort: localAgentHostPort,
			CollectorEndpoint:  collectorEndpoint,
		},
		ServiceName: serviceName,
	}
	if samplerType != "" {
		conf.Sampler = &jaegerconfig.SamplerConfig{
			Type:  samplerType,
			Param: samplerParam,
		}
	}
	zipkinPropagator := zipkin.NewZipkinB3HTTPHeaderPropagator()
	jaegerTracer, _, err := conf.NewTracer(
		jaegerconfig.Injector(opentracing.HTTPHeaders, zipkinPropagator),
		jaegerconfig.Extractor(opentracing.HTTPHeaders, zipkinPropagator),
		jaegerconfig.ZipkinSharedRPCSpan(true),
	)
	if err != nil {
		log.Error(context.Background(), "could not initialize jaeger", err)
	}
	opentracing.SetGlobalTracer(jaegerTracer)
}

func setupHystrix() {
	promC := hystrixprometheus.NewPrometheusCollector("hystrix", nil, prometheus.DefBuckets)
	metricCollector.Registry.Register(promC.Collector)
}
