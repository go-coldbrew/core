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
package core
