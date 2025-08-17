package config

// Config is the configuration for the Coldbrew server
// It is populated from environment variables and has sensible defaults for all fields so that you can just use it as is without any configuration
// The following environment variables are supported and can be used to override the defaults for the fields
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
	// SwaggerURL is the URL at which swagger is served, defaults to /swagger/
	SwaggerURL string `envconfig:"SWAGGER_URL" default:"/swagger/"`
	// Should we disable go debug at /debug/, defaults to false
	DisableDebug bool `envconfig:"DISABLE_DEBUG" default:"false"`
	// Should we disable prometheus at /metrics, defaults to false
	DisablePormetheus bool `envconfig:"DISABLE_PROMETHEUS" default:"false"`
	// Enables grpc request histograms in prometheus reporting
	EnablePrometheusGRPCHistogram bool `envconfig:"ENABLE_PROMETHEUS_GRPC_HISTOGRAM" default:"true"`
	// The License key for NewRelic metrics reporting
	NewRelicLicenseKey string `envconfig:"NEW_RELIC_LICENSE_KEY" default:""`
	// Enable NewRelic Distributed Tracing
	NewRelicDistributedTracing bool `envconfig:"NEW_RELIC_DISTRIBUTED_TRACING" default:"true"`
	// Enable new relic opentelemetry
	NewRelicOpentelemetry bool `envconfig:"NEW_RELIC_OPENTELEMETRY" default:"true"`
	// Sampling ratio for NR opentelemetry
	NewRelicOpentelemetrySample float64 `envconfig:"NEW_RELIC_OPENTELEMETRY_SAMPLE" default:"0.2"`
	// The name of the application in NewRelic
	NewRelicAppname string `envconfig:"NEW_RELIC_APPNAME" default:""`
	// DSN for reporting errors to sentry
	SentryDSN string `envconfig:"SENTRY_DSN" default:""`
	// Name of this release
	ReleaseName string `envconfig:"RELEASE_NAME" default:""`
	// When set disable the GRPC reflecttion server which can be useful for tools like grpccurl, default false
	DisableGRPCReflection bool `envconfig:"DISABLE_GRPC_REFLECTION" default:"false"`
	// Trace header, when this HTTP header is present CB will add the value to log/trace contexts
	TraceHeaderName string `envconfig:"TRACE_HEADER_NAME" default:"x-trace-id"`
	// [Deprecated] - please use HTTPHeaderPrefixes instead
	HTTPHeaderPrefix string `envconfig:"HTTP_HEADER_PREFIX" default:""`
	// When we match one of the HTTP header prefix configured in this list,
	// we forward append the values to grpc metadata. If the deprecated HTTPHeaderPrefix
	// is set, it will only be used if this field is not configured
	HTTPHeaderPrefixes []string `envconfig:"HTTP_HEADER_PREFIXES" default:""`
	// Should we log calls to GRPC reflection API, defaults to true
	DoNotLogGRPCReflection bool `envconfig:"DO_NOT_LOG_GRPC_REFLECTION" default:"true"`
	// Should we disable signal handler, defaults to false and CB handles all SIG_INT/SIG_TERM
	DisableSignalHandler bool `envconfig:"DISABLE_SIGNAL_HANDLER" default:"false"`
	// Duration for which CB will wait for calls to complete before shutting down the server
	ShutdownDurationInSeconds int `envconfig:"SHUTDOWN_DURATION_IN_SECONDS" default:"15"`
	// Duration for which CB will wait for healthcheck fail to be propagated before initiating server shutdown
	// once shutdown is initiated all new calls will fail
	HealthcheckWaitDurationInSeconds int `envconfig:"GRPC_GRACEFUL_DURATION_IN_SECONDS" default:"7"`
	// UseJSONBuiltinMarshaller switches marshaler for application/json to encoding/json
	UseJSONBuiltinMarshaller bool `envconfig:"USE_JSON_BUILTIN_MARSHALLER" default:"false"`
	// JSONBuiltinMarshallerMime specifies the Content-Type/Accept header for use by the json builtin marshaler
	JSONBuiltinMarshallerMime string `envconfig:"JSON_BUILTIN_MARSHALLER_MIME" default:"application/json"`
	// MaxConnectionIdle is a duration for the amount of time after which an
	// idle connection would be closed by sending a GoAway. Idleness duration is
	// defined since the most recent time the number of outstanding RPCs became
	// zero or the connection establishment.
	// https://github.com/grpc/grpc-go/blob/v1.48.0/keepalive/keepalive.go#L50
	GRPCServerMaxConnectionIdleInSeconds int `envconfig:"GRPC_SERVER_MAX_CONNECTION_IDLE_IN_SECONDS"`
	// MaxConnectionAge is a duration for the maximum amount of time a
	// connection may exist before it will be closed by sending a GoAway. A
	// random jitter of +/-10% will be added to MaxConnectionAge to spread out
	// connection storms.
	// https://github.com/grpc/grpc-go/blob/v1.48.0/keepalive/keepalive.go#L50
	GRPCServerMaxConnectionAgeInSeconds int `envconfig:"GRPC_SERVER_MAX_CONNECTION_AGE_IN_SECONDS"`
	// MaxConnectionAgeGrace is an additive period after MaxConnectionAge after
	// which the connection will be forcibly closed.
	// https://github.com/grpc/grpc-go/blob/v1.48.0/keepalive/keepalive.go#L50
	GRPCServerMaxConnectionAgeGraceInSeconds int `envconfig:"GRPC_SERVER_MAX_CONNECTION_AGE_GRACE_IN_SECONDS"`

	// DisableAutoMaxProcs disables the automatic setting of GOMAXPROCS
	// This is useful when running in a container where the container runtime sets GOMAXPROCS for you already
	DisableAutoMaxProcs bool `envconfig:"DISABLE_AUTO_MAX_PROCS" default:"false"`

	// GRPCTLSKeyFile and GRPCTLSCertFile are the paths to the key and cert files for the GRPC server
	// If these are set, the server will be started with TLS enabled
	GRPCTLSKeyFile string `envconfig:"GRPC_TLS_KEY_FILE"`
	// GRPCTLSCertFile an GRPCTLSKeyFile are the paths to the key and cert files for the GRPC server
	// If these are set, the server will be started with TLS enabled
	GRPCTLSCertFile string `envconfig:"GRPC_TLS_CERT_FILE"`
	// GRPCTLSInsecureSkipVerify is used to skip verification of the server's certificate chain and host name
	// Only set this to true if you are sure you want to disable TLS verification for the server
	GRPCTLSInsecureSkipVerify bool `envconfig:"GRPC_TLS_INSECURE_SKIP_VERIFY" default:"false"`
	// DisableVTProtobuf disables the use of the vtprotobuf marshaller and unmarshaller for GRPC
	// https://github.com/planetscale/vtprotobuf
	DisableVTProtobuf bool `envconfig:"DISABLE_VT_PROTOBUF" default:"false"`
	// GRPCMaxSendMsgSize and GRPCMaxRecvMsgSize are the maximum message
	// sizes for sending and receiving messages over GRPC
	GRPCMaxSendMsgSize int `envconfig:"GRPC_MAX_SEND_MSG_SIZE" default:"0"`       // Unlimited
	GRPCMaxRecvMsgSize int `envconfig:"GRPC_MAX_RECV_MSG_SIZE" default:"4194304"` // 4MB
}
