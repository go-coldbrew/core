package config

type Config struct {
	// App configuration
	GRPCPort int    `envconfig:"GRPC_PORT" default:"9090"`
	HTTPPort int    `envconfig:"HTTP_PORT" default:"9091"`
	AppName  string `envconfig:"APP_NAME" default:"ColdBrewService"`
	LogLevel string `envconfig:"LOG_LEVEL" default:"info"`
	JSONLogs bool   `envconfig:"JSON_LOGS" default:"true"`
}
