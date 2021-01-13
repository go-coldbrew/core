package core

import (
	"context"
	"strings"

	"github.com/go-coldbrew/errors/notifier"
	"github.com/go-coldbrew/log"
	nrutil "github.com/go-coldbrew/tracing/newrelic"
	newrelic "github.com/newrelic/go-agent/v3/newrelic"
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
