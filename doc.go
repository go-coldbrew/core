// Package core is the main entry point for the ColdBrew microservice framework.
// It creates a gRPC server with an HTTP gateway (via grpc-gateway), wires health checks,
// Prometheus metrics, pprof endpoints, signal handling, graceful shutdown, and all
// interceptors. Services implement the [CBService] interface to register their gRPC
// and HTTP handlers.
//
// ColdBrew builds on proven open-source libraries:
//
//   - [github.com/grpc-ecosystem/grpc-gateway] — REST gateway for gRPC services
//   - [github.com/prometheus/client_golang] — Prometheus metrics
//   - [go.opentelemetry.io/otel] — Distributed tracing via OpenTelemetry
//   - [github.com/newrelic/go-agent] — New Relic APM integration
//
// # Usage
//
//	cb := core.New(config.Config{
//	    GRPCPort:    "9090",
//	    HTTPPort:    "9091",
//	    ServiceName: "my-service",
//	})
//	cb.SetService(myService)
//	cb.Run()
//
// For full documentation, visit https://docs.coldbrew.cloud
package core
