module github.com/go-coldbrew/core

go 1.15

require (
	github.com/NYTimes/gziphandler v1.1.1
	github.com/afex/hystrix-go v0.0.0-20180502004556-fa1af6a1f4f5
	github.com/go-coldbrew/errors v0.2.1
	github.com/go-coldbrew/hystrixprometheus v0.1.0
	github.com/go-coldbrew/interceptors v0.1.7
	github.com/go-coldbrew/log v0.2.2
	github.com/go-coldbrew/options v0.2.3
	github.com/go-coldbrew/tracing v0.0.3
	github.com/go-logfmt/logfmt v0.6.0 // indirect
	github.com/golangci/golangci-lint v1.52.2
	github.com/grpc-ecosystem/go-grpc-middleware v1.4.0
	github.com/grpc-ecosystem/go-grpc-prometheus v1.2.0
	github.com/grpc-ecosystem/grpc-gateway/v2 v2.15.2
	github.com/jaegertracing/jaeger-lib v2.4.1+incompatible
	github.com/newrelic/go-agent/v3 v3.21.1
	github.com/newrelic/go-agent/v3/integrations/nrgrpc v1.3.2 // indirect
	github.com/opentracing/opentracing-go v1.2.0
	github.com/princjef/gomarkdoc v0.4.1
	github.com/prometheus/client_golang v1.15.1
	github.com/prometheus/common v0.43.0 // indirect
	github.com/uber/jaeger-client-go v2.30.0+incompatible
	github.com/uber/jaeger-lib v2.4.1+incompatible // indirect
	go.opentelemetry.io/otel v1.15.1
	go.opentelemetry.io/otel/bridge/opentracing v1.15.1
	go.opentelemetry.io/otel/exporters/otlp/otlptrace v1.15.1
	go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc v1.15.1
	go.opentelemetry.io/otel/sdk v1.15.1
	go.uber.org/atomic v1.11.0 // indirect
	golang.org/x/sys v0.8.0 // indirect
	google.golang.org/genproto v0.0.0-20230410155749-daa745c078e1 // indirect
	google.golang.org/grpc v1.55.0
)
