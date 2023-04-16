// package core is the core module for cold brew
// and provides the base implementation for Cold Brew.
//
// The idea behind cold brew is simple, we want to reuse as many good components
// that we can by providing only a thin wrapper around them if needed.
//
// The components in use by cold brew currently are
//
//	github.com/grpc-ecosystem/grpc-gateway
//	github.com/prometheus/client_golang
//	github.github.com/afex/hystrix-go
//	github.com/opentracing/opentracing-go
//	github.com/newrelic/go-agent
//
// The core module provides the base implementation for Cold Brew.
// It provides the following features
//
//   - A base implementation for a gRPC server
//   - A base implementation for a gRPC gateway
//   - A base implementation for health check
//   - A base implementation for metrics
//   - A base implementation for a circuit breaker
//   - A base implementation for a tracing
//   - A base implementation for a new relic
//   - A base implementation for a logger
//   - A base implementation for a gRPC server reflection
//
// The core module is the base module for cold brew and provides the base
// implementation for Cold Brew. It works in conjunction with the other modules to provide the full functionality of Cold Brew.
// To get started with Cold Brew, you can use cookiecutter to generate a new project from the template. The template can be found at
// https://github.com/go-coldbrew/cookiecutter-coldbrew
package core
