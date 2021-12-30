package config

import (
	ffConfig "github.com/go-coldbrew/feature-flags/config"
)

type Config struct {
	// Host to listen on
	ListenHost string `envconfig:"LISTEN_HOST" default:"0.0.0.0"`
	// GRPC Port, defaults to 9090
	GRPCPort int `envconfig:"GRPC_PORT" default:"9090"`
	// HTTP Port, defaults to 9091
	HTTPPort int `envconfig:"HTTP_PORT" default:"9091"`
	// Name of the Application
	AppName string `envconfig:"APP_NAME" default:""`
	// Environment e.g. Production / Integration / Development
	Environment string `envconfig:"ENVIRONMENT" default:""`
	// LogLevel to print, default to info
	LogLevel string `envconfig:"LOG_LEVEL" default:"info"`
	// Should logs be emitted in json format, defaults to true
	JSONLogs bool `envconfig:"JSON_LOGS" default:"true"`
	// Should we disable swagger at /swagger/, defaults to false
	DisableSwagger bool `envconfig:"DISABLE_SWAGGER" default:"false"`
	// Should we disable go debug at /debug/, defaults to false
	DisableDebug bool `envconfig:"DISABLE_DEBUG" default:"false"`
	// Should we disable prometheus at /metrics, defaults to false
	DisablePormetheus bool `envconfig:"DISABLE_PROMETHEUS" default:"false"`
	// Enables grpc request histograms in prometheus reporting
	EnablePrometheusGRPCHistogram bool `envconfig:"ENABLE_PROMETHEUS_GRPC_HISTOGRAM" default:"false"`
	// The License key for NewRelic metrics reporting
	NewRelicLicenseKey string `envconfig:"NEW_RELIC_LICENSE_KEY" default:""`
	// DSN for reporting errors to sentry
	SentryDSN string `envconfig:"SENTRY_DSN" default:""`
	// Name of this release
	ReleaseName string `envconfig:"RELEASE_NAME" default:""`
	// When set disable the GRPC reflecttion server which can be useful for tools like grpccurl, default false
	DisableGRPCReflection bool `envconfig:"DISABLE_GRPC_REFLECTION" default:"false"`
	// Trace header, when this HTTP header is present CB will add the value to log/trace contexts
	TraceHeaderName string `envconfig:"TRACE_HEADER_NAME" default:"x-trace-id"`
	// When we match this HTTP header prefix, we forward append the values to grpc metadata
	HTTPHeaderPrefix string `envconfig:"HTTP_HEADER_PREFIX" default:""`
	// Should we log calls to GRPC reflection API, defaults to true
	DoNotLogGRPCReflection bool `envconfig:"DO_NOT_LOG_GRPC_REFLECTION" default:"true"`
	// Should we disable signal handler, defaults to false and CB handles all SIG_INT/SIG_TERM
	DisableSignalHandler bool `envconfig:"DISABLE_SIGNAL_HANDLER" default:"false"`
	// Duration for which CB will wait for calls to complete before shutting down the server
	ShutdownDurationInSeconds int `envconfig:"SHUTDOWN_DURATION_IN_SECONDS" default:"10"`
	// UseJSONBuiltinMarshaller switches marshaler for application/json to encoding/json
	UseJSONBuiltinMarshaller bool `envconfig:"USE_JSON_BUILTIN_MARSHALLER" default:"false"`
	// JSONBuiltinMarshallerMime specifies the Content-Type/Accept header for use by the json builtin marshaler
	JSONBuiltinMarshallerMime string `envconfig:"JSON_BUILTIN_MARSHALLER_MIME" default:"application/json"`

	FeatureFlagConfig ffConfig.Config
}
