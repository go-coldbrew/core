package core

import "github.com/go-coldbrew/workers"

// Run-level options applied when core.Run() invokes workers.Run. Mutated
// during init via AddWorkerRunOptions; not concurrency-safe by design (same
// contract as the OTEL setters in core.go).
var workerRunOpts []workers.RunOption

// AddWorkerRunOptions appends [workers.RunOption] values applied when
// core.Run() invokes [workers.Run]. Use this to configure framework-wide
// worker behaviour: metrics, run-level interceptors, default jitter, etc.
// Must be called during init, before Run(). Not concurrency-safe.
//
// By default, core wires a Prometheus metrics implementation using the
// service's APP_NAME unless DISABLE_PROMETHEUS=true or APP_NAME is empty.
// Pass [workers.WithMetrics] here to override that default; a later
// WithMetrics wins because workers.WithMetrics overwrites runConfig.metrics
// on each apply.
func AddWorkerRunOptions(opts ...workers.RunOption) {
	workerRunOpts = append(workerRunOpts, opts...)
}

// buildWorkerRunOpts assembles the option slice passed to workers.Run. The
// default Prometheus metrics (when enabled) is prepended so that any
// user-supplied workers.WithMetrics overrides it.
func (c *cb) buildWorkerRunOpts() []workers.RunOption {
	opts := make([]workers.RunOption, 0, len(workerRunOpts)+1)
	if !c.config.DisablePrometheus && !c.config.DisablePormetheus && c.config.AppName != "" { //nolint:staticcheck // intentional use of deprecated field for backward compatibility
		opts = append(opts, workers.WithMetrics(workers.NewPrometheusMetrics(c.config.AppName)))
	}
	opts = append(opts, workerRunOpts...)
	return opts
}
