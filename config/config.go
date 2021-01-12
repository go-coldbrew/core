package config

type Config struct {
	// App configuration
	ListenHost         string `envconfig:"LISTEN_HOST" default:"0.0.0.0"`
	GRPCPort           int    `envconfig:"GRPC_PORT" default:"9090"`
	HTTPPort           int    `envconfig:"HTTP_PORT" default:"9091"`
	AppName            string `envconfig:"APP_NAME" default:"ColdBrewService"`
	LogLevel           string `envconfig:"LOG_LEVEL" default:"info"`
	JSONLogs           bool   `envconfig:"JSON_LOGS" default:"true"`
	DisableSwagger     bool   `envconfig:"DISABLE_SWAGGER" default:"false"`
	DisableDebug       bool   `envconfig:"DISABLE_DEBUG" default:"false"`
	DisablePormetheus  bool   `envconfig:"DISABLE_PROMETHEUS" default:"false"`
	NewRelicLicenseKey string `envconfig:"NEW_RELIC_LICENSE_KEY" default:""`
}
