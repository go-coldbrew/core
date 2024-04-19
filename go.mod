module github.com/go-coldbrew/core

go 1.15

require (
	github.com/NYTimes/gziphandler v1.1.1
	github.com/afex/hystrix-go v0.0.0-20180502004556-fa1af6a1f4f5
	github.com/go-coldbrew/errors v0.1.1
	github.com/go-coldbrew/hystrixprometheus v0.1.0
	github.com/go-coldbrew/interceptors v0.1.4
	github.com/go-coldbrew/log v0.1.0
	github.com/go-coldbrew/options v0.1.0
	github.com/go-coldbrew/tracing v0.0.1
	github.com/go-kit/log v0.2.1 // indirect
	github.com/grpc-ecosystem/go-grpc-middleware v1.3.0
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.11.1
	github.com/jaegertracing/jaeger-lib v2.4.1+incompatible
	github.com/newrelic/go-agent/v3 v3.18.0
	github.com/opentracing/opentracing-go v1.2.0
	github.com/prometheus/client_golang v1.12.2
	github.com/prometheus/common v0.37.0 // indirect
	github.com/prometheus/procfs v0.8.0 // indirect
	github.com/uber/jaeger-client-go v2.30.0+incompatible
	github.com/uber/jaeger-lib v2.4.1+incompatible // indirect
	go.opentelemetry.io/otel v1.9.0
	go.opentelemetry.io/otel/bridge/opentracing v1.9.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.9.0
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.9.0
	go.opentelemetry.io/otel/sdk v1.9.0
	go.uber.org/automaxprocs v1.5.3
	golang.org/x/net v0.23.0 // indirect
	google.golang.org/genproto v0.0.0-20220802133213-ce4fa296bf78 // indirect
	google.golang.org/grpc v1.48.0
)
