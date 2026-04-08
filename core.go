package core

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/go-coldbrew/core/config"
	"github.com/go-coldbrew/interceptors"
	"github.com/go-coldbrew/log"
	"github.com/go-coldbrew/log/loggers"
	"github.com/go-coldbrew/options"
	grpcprom "github.com/grpc-ecosystem/go-grpc-middleware/providers/prometheus"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/klauspost/compress/gzhttp"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	_ "google.golang.org/grpc/encoding/gzip"
	experimental "google.golang.org/grpc/experimental/opentelemetry"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/stats"
	grpcotel "google.golang.org/grpc/stats/opentelemetry"
)

// SupportPackageIsVersion1 is a compile-time assertion constant.
// Downstream packages reference this to enforce version compatibility.
const SupportPackageIsVersion1 = true

// Compile-time version compatibility checks.
const _ = interceptors.SupportPackageIsVersion1

type cb struct {
	svc            []CBService
	openAPIHandler http.Handler
	config         config.Config
	closers        []io.Closer
	grpcServer     *grpc.Server
	httpServer     *http.Server
	cancelFunc     context.CancelFunc
	gracefulWait   sync.WaitGroup
	creds          credentials.TransportCredentials
	unixSocketPath string
}

func (c *cb) SetService(svc CBService) error {
	if svc == nil {
		return errors.New("service is nil")
	}
	c.svc = append(c.svc, svc)
	return nil
}

// SetOpenAPIHandler sets the openapi handler
// This is used to serve the openapi spec
// This is optional
func (c *cb) SetOpenAPIHandler(handler http.Handler) {
	c.openAPIHandler = handler
}

// parseHeaders parses a comma-separated string of key=value pairs into a map
// Example: "key1=value1,key2=value2" -> map[string]string{"key1": "value1", "key2": "value2"}
func parseHeaders(headerString string) map[string]string {
	headers := make(map[string]string)
	if headerString == "" {
		return headers
	}

	pairs := strings.SplitSeq(headerString, ",")
	for pair := range pairs {
		kv := strings.SplitN(strings.TrimSpace(pair), "=", 2)
		if len(kv) == 2 {
			headers[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
		} else {
			log.Warn(context.Background(), "msg", "Ignoring malformed header pair Expected format 'key=value'", "pair", pair)
		}
	}
	return headers
}

// processConfig processes the config and sets up the logger, newrelic, sentry, environment, release name, OpenTelemetry tracing, hystrix prometheus and signal handler
func (c *cb) processConfig() {
	if err := SetupLogger(c.config.LogLevel, c.config.JSONLogs); err != nil {
		log.Error(context.Background(), "msg", "failed to setup logger", "err", err)
	}

	if !c.config.DisableVTProtobuf {
		InitializeVTProto()
	}
	nrName := c.config.AppName
	if nrName == "" {
		nrName = c.config.AppName
	}
	if !c.config.DisableAutoMaxProcs {
		SetupAutoMaxProcs()
	}
	// Auto-disable NewRelic when no license key is configured to avoid
	// interceptor overhead for services that don't use NR.
	if !c.config.DisableNewRelic && strings.TrimSpace(c.config.NewRelicLicenseKey) == "" {
		c.config.DisableNewRelic = true
	}
	if !c.config.DisableNewRelic {
		err := SetupNewRelic(nrName, c.config.NewRelicLicenseKey, c.config.NewRelicDistributedTracing)
		if err != nil {
			log.Error(context.Background(), "Error setting up NewRelic tracing", "error", err)
		}
	}
	SetupSentry(c.config.SentryDSN)
	SetupEnvironment(c.config.Environment)
	SetupReleaseName(c.config.ReleaseName)
	SetupHystrixPrometheus()
	ConfigureInterceptors(c.config.DoNotLogGRPCReflection, c.config.TraceHeaderName, c.config.ResponseTimeLogLevel, c.config.ResponseTimeLogErrorOnly, c.config.GRPCServerDefaultTimeoutInSeconds)
	if !c.config.DisableSignalHandler {
		dur := time.Second * 10
		if c.config.ShutdownDurationInSeconds > 0 {
			dur = time.Second * time.Duration(c.config.ShutdownDurationInSeconds)
		}
		startSignalHandler(c, dur)
	}
	if c.config.DisableProtoValidate {
		interceptors.SetDisableProtoValidate(true)
	}
	if c.config.EnablePrometheusGRPCHistogram {
		if len(c.config.PrometheusGRPCHistogramBuckets) > 0 {
			interceptors.SetServerMetricsOptions(
				grpcprom.WithServerHandlingTimeHistogram(
					grpcprom.WithHistogramBuckets(c.config.PrometheusGRPCHistogramBuckets),
				),
			)
		} else {
			interceptors.SetServerMetricsOptions(
				grpcprom.WithServerHandlingTimeHistogram(),
			)
		}
	}

	// Warn if deprecated OpenTracing bridge env var is still set.
	if c.config.OTLPUseOpenTracingBridge { //nolint:staticcheck // reading deprecated field to emit warning
		log.Warn(context.Background(), "msg", "OTLP_USE_OPENTRACING_BRIDGE is set but OpenTracing bridge has been removed; this setting is ignored")
	}

	// Setup OpenTelemetry - custom OTLP takes precedence over New Relic
	prevTP := otelTracerProvider // track whether this call initializes a new provider
	var otlpConfig OTLPConfig
	if c.config.OTLPEndpoint != "" {
		headers := parseHeaders(c.config.OTLPHeaders)
		otlpConfig = OTLPConfig{
			Endpoint:       c.config.OTLPEndpoint,
			Headers:        headers,
			ServiceName:    c.config.AppName,
			ServiceVersion: c.config.ReleaseName,
			SamplingRatio:  c.config.OTLPSamplingRatio,
			Compression:    c.config.OTLPCompression,
			Insecure:       c.config.OTLPInsecure,
		}
		if err := SetupOpenTelemetry(otlpConfig); err != nil {
			log.Error(context.Background(), "msg", "Failed to setup custom OTLP", "err", err)
		}
	} else if c.config.NewRelicOpentelemetry {
		err := SetupNROpenTelemetry(
			nrName,
			c.config.NewRelicLicenseKey,
			c.config.ReleaseName,
			c.config.NewRelicOpentelemetrySample,
		)
		if err != nil {
			log.Error(context.Background(), "msg", "Failed to setup New Relic OpenTelemetry", "err", err)
		}
		// Build otlpConfig for NR path so OTEL metrics can reuse the endpoint.
		// Only populate when the license key is non-empty (SetupNROpenTelemetry
		// no-ops without it, so metrics would just get auth failures).
		if strings.TrimSpace(c.config.NewRelicLicenseKey) != "" {
			otlpConfig = OTLPConfig{
				Endpoint:       nrOTLPEndpoint,
				Headers:        map[string]string{"api-key": c.config.NewRelicLicenseKey},
				ServiceName:    nrName,
				ServiceVersion: c.config.ReleaseName,
				Compression:    "gzip",
			}
		}
	}

	// Register TracerProvider for graceful shutdown — only if this
	// processConfig() call actually initialized a new one.
	if tp := otelTracerProvider; tp != nil && tp != prevTP {
		c.closers = append(c.closers, closerFunc(func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return tp.Shutdown(ctx)
		}))
	}

	if c.config.EnableOTELMetrics {
		if otlpConfig.Endpoint == "" {
			log.Error(context.Background(), "msg", "ENABLE_OTEL_METRICS is true but no OTLP endpoint is configured; OTEL metrics will not be exported")
		} else {
			interval := time.Duration(c.config.OTELMetricsInterval) * time.Second
			mp, err := SetupOTELMetrics(otlpConfig, interval)
			if err != nil {
				log.Error(context.Background(), "msg", "Failed to setup OTEL metrics", "err", err)
			} else {
				otelMeterProvider = mp
				c.closers = append(c.closers, closerFunc(func() error {
					ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
					defer cancel()
					return mp.Shutdown(ctx)
				}))
			}
		}
	}

	// Record legacy preference so getGRPCServerOptions/initHTTP respect it
	// even if SetOTELOptions() was called during init.
	otelUseLegacy = c.config.OTELUseLegacyInstrumentation

	// Build native stats/opentelemetry options unless user already called
	// SetOTELOptions() or legacy instrumentation is requested.
	if !otelUseLegacy && !otelGRPCOptionsSet {
		otelGRPCOptions = buildOTELOptions()
		otelGRPCOptionsSet = true
	}
}

// statusRecorder wraps http.ResponseWriter to capture the final HTTP status code.
// It records the first status >= 200, plus 101 Switching Protocols (which is
// terminal). Other 1xx statuses are informational and skipped.
// Unwrap() is provided for http.ResponseController (Go 1.20+) to access optional
// interfaces (http.Flusher, http.Hijacker, etc.) from the underlying writer.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

var statusRecorderPool = sync.Pool{
	New: func() any {
		return &statusRecorder{}
	},
}

func (sr *statusRecorder) WriteHeader(code int) {
	if !sr.wroteHeader && (code >= 200 || code == http.StatusSwitchingProtocols) {
		sr.status = code
		sr.wroteHeader = true
	}
	sr.ResponseWriter.WriteHeader(code)
}

func (sr *statusRecorder) Write(b []byte) (int, error) {
	if !sr.wroteHeader {
		sr.status = http.StatusOK
		sr.wroteHeader = true
	}
	return sr.ResponseWriter.Write(b)
}

// Unwrap returns the underlying ResponseWriter so that http.ResponseController
// and middleware can access optional interfaces (http.Flusher, http.Hijacker, etc.).
func (sr *statusRecorder) Unwrap() http.ResponseWriter {
	return sr.ResponseWriter
}

// endSpan records the HTTP status code on the span, marks it as error for 5xx, and ends it.
func endSpan(span oteltrace.Span, rec *statusRecorder) {
	span.SetAttributes(semconv.HTTPResponseStatusCode(rec.status))
	if rec.status >= 500 {
		span.SetStatus(codes.Error, http.StatusText(rec.status))
	}
	span.End()
}

// httpSpanAttributes returns the OTEL attributes for an incoming HTTP request,
// omitting empty-valued attributes (e.g. scheme behind a reverse proxy).
func httpSpanAttributes(r *http.Request) []attribute.KeyValue {
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil {
		host = r.Host
	}
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	// Pre-allocate for the common case: method + path + host + scheme + optional port/query.
	var attrBuf [6]attribute.KeyValue
	attrBuf[0] = semconv.HTTPRequestMethodKey.String(r.Method)
	attrBuf[1] = semconv.URLPath(r.URL.Path)
	attrs := attrBuf[:2]
	if host != "" {
		attrs = append(attrs, semconv.ServerAddress(host))
	}
	if port != "" {
		if p, err := strconv.Atoi(port); err == nil {
			attrs = append(attrs, semconv.ServerPort(p))
		}
	}
	if r.URL.RawQuery != "" {
		attrs = append(attrs, semconv.URLQuery(r.URL.RawQuery))
	}
	scheme := r.URL.Scheme
	if scheme == "" {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme = proto
		} else if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	attrs = append(attrs, semconv.URLScheme(scheme))
	return attrs
}

// tracingWrapper is a middleware that creates a new OTEL span for each incoming HTTP request.
// It extracts any propagated trace context from the request headers and, for non-filtered
// methods, starts a server span that is attached to the request context.
func tracingWrapper(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))

		if interceptors.FilterMethodsFunc(ctx, r.URL.Path) {
			var serverSpan oteltrace.Span
			ctx, serverSpan = otel.Tracer("coldbrew-http").Start(ctx, r.Method,
				oteltrace.WithSpanKind(oteltrace.SpanKindServer),
				oteltrace.WithAttributes(httpSpanAttributes(r)...),
			)
			rec := statusRecorderPool.Get().(*statusRecorder)
			rec.ResponseWriter = w
			rec.status = http.StatusOK
			rec.wroteHeader = false
			w = rec
			defer func() {
				if recovered := recover(); recovered != nil {
					if !rec.wroteHeader {
						rec.status = http.StatusInternalServerError
					}
					serverSpan.RecordError(fmt.Errorf("panic: %v", recovered))
					serverSpan.SetStatus(codes.Error, "panic")
					endSpan(serverSpan, rec)
					rec.ResponseWriter = nil
					statusRecorderPool.Put(rec)
					panic(recovered)
				}
				endSpan(serverSpan, rec)
				rec.ResponseWriter = nil
				statusRecorderPool.Put(rec)
			}()
		}

		_, han := interceptors.NRHttpTracer("", h.ServeHTTP)
		// No-op with empty key (returns ctx unchanged). Kept for historical
		// reasons; the subsequent AddToLogContext call initializes RequestContext.
		ctx = options.AddToOptions(ctx, "", "")
		ctx = loggers.AddToLogContext(ctx, "httpPath", r.URL.Path)
		han(w, r.WithContext(ctx))
	})
}

// spanRouteMiddleware is a grpc-gateway middleware that updates the OTEL span
// name and http.route attribute with the matched route pattern after routing.
// It uses runtime.HTTPPattern (the Pattern struct set by handleHandler) rather
// than runtime.HTTPPathPattern (the string set later inside AnnotateContext).
func spanRouteMiddleware(next runtime.HandlerFunc) runtime.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request, pathParams map[string]string) {
		if pattern, ok := runtime.HTTPPattern(r.Context()); ok {
			route := pattern.String()
			span := oteltrace.SpanFromContext(r.Context())
			span.SetName(r.Method + " " + route)
			span.SetAttributes(semconv.HTTPRoute(route))
		}
		next(w, r, pathParams)
	}
}

// getCustomHeaderMatcher returns a matcher that matches the given header and prefix
func getCustomHeaderMatcher(prefixes []string, header string) func(string) (string, bool) {
	header = strings.ToLower(header)
	return func(key string) (string, bool) {
		key = strings.ToLower(key)

		if key == header {
			return key, true
		} else if len(prefixes) > 0 {
			for _, prefix := range prefixes {
				if len(prefix) > 0 && strings.HasPrefix(key, strings.ToLower(prefix)) {
					return key, true
				}
			}
		}

		return runtime.DefaultHeaderMatcher(key)
	}
}

func (c *cb) initHTTP(ctx context.Context) (*http.Server, error) {
	// Register gRPC server endpoint
	// Note: Make sure the gRPC server is running properly and accessible
	grpcServerEndpoint := fmt.Sprintf("%s:%d", c.config.ListenHost, c.config.GRPCPort)

	pMar := &runtime.ProtoMarshaller{}

	allowedHttpHeaderPrefixes := c.config.HTTPHeaderPrefixes
	// maintaining backward compatibility
	if len(c.config.HTTPHeaderPrefix) > 0 && len(allowedHttpHeaderPrefixes) == 0 {
		allowedHttpHeaderPrefixes = []string{c.config.HTTPHeaderPrefix}
	}

	muxOpts := []runtime.ServeMuxOption{
		runtime.WithIncomingHeaderMatcher(
			getCustomHeaderMatcher(allowedHttpHeaderPrefixes, c.config.TraceHeaderName),
		),
		runtime.WithMarshalerOption("application/proto", pMar),
		runtime.WithMarshalerOption("application/protobuf", pMar),
		runtime.WithMiddlewares(spanRouteMiddleware),
	}

	if c.config.UseJSONBuiltinMarshaller {
		muxOpts = append(
			muxOpts,
			runtime.WithMarshalerOption(c.config.JSONBuiltinMarshallerMime, &runtime.JSONBuiltin{}),
		)
	}

	mux := runtime.NewServeMux(muxOpts...)

	creds := c.creds
	if creds == nil {
		creds = insecure.NewCredentials()
	}

	// Use unix socket for the gateway's internal connection when available.
	// Only when TLS is not configured — grpc.Server applies TLS to all listeners,
	// so insecure dial would fail the handshake.
	dialEndpoint := grpcServerEndpoint
	dialCreds := creds
	if c.unixSocketPath != "" && c.creds == nil {
		dialEndpoint = "unix:" + c.unixSocketPath
		dialCreds = insecure.NewCredentials()
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(dialCreds),
	}
	// Use native stats/opentelemetry unless legacy mode is forced via config.
	if !otelUseLegacy && otelGRPCOptionsSet {
		opts = append(opts, grpcotel.DialOption(otelGRPCOptions))
	} else {
		opts = append(opts, grpc.WithStatsHandler(otelgrpc.NewClientHandler(otelGRPCClientOpts...)))
	}
	opts = append(opts, grpc.WithUnaryInterceptor(
		interceptors.DefaultClientInterceptor(
			interceptors.WithoutHystrix(),
		),
	))
	// Mirror configured limits on the client side used by the gateway.
	if c.config.GRPCMaxRecvMsgSize > 0 {
		opts = append(opts,
			grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(c.config.GRPCMaxRecvMsgSize)),
		)
	}
	if c.config.GRPCMaxSendMsgSize > 0 {
		opts = append(opts,
			grpc.WithDefaultCallOptions(grpc.MaxCallSendMsgSize(c.config.GRPCMaxSendMsgSize)),
		)
	}
	for _, s := range c.svc {
		if err := s.InitHTTP(ctx, mux, dialEndpoint, opts); err != nil {
			return nil, err
		}
	}

	// Start HTTP server (and proxy calls to gRPC server endpoint)
	gatewayAddr := fmt.Sprintf("%s:%d", c.config.ListenHost, c.config.HTTPPort)
	promHandler := promhttp.Handler()
	gzipHandler := http.Handler(tracingWrapper(mux))
	if !c.config.DisableHTTPCompression {
		wrapper, err := gzhttp.NewWrapper(gzhttp.MinSize(c.config.HTTPCompressionMinSize))
		if err != nil {
			return nil, fmt.Errorf("failed to create compression handler: %w", err)
		}
		gzipHandler = wrapper(gzipHandler)
	}
	adminMux := http.NewServeMux()
	if !c.config.DisableDebug {
		adminMux.Handle("/debug/pprof/cmdline", http.HandlerFunc(pprof.Cmdline))
		adminMux.Handle("/debug/pprof/profile", http.HandlerFunc(pprof.Profile))
		adminMux.Handle("/debug/pprof/symbol", http.HandlerFunc(pprof.Symbol))
		adminMux.Handle("/debug/pprof/trace", http.HandlerFunc(pprof.Trace))
		adminMux.Handle("/debug/pprof/", http.HandlerFunc(pprof.Index))
	}
	if !(c.config.DisablePrometheus || c.config.DisablePormetheus) { //nolint:staticcheck // intentional use of deprecated field for backward compatibility
		adminMux.Handle("/metrics", promHandler)
		adminMux.Handle("/metrics/", promHandler) // preserve HasPrefix semantics
	}
	if !c.config.DisableSwagger && c.openAPIHandler != nil {
		swaggerURL := c.config.SwaggerURL
		if !strings.HasPrefix(swaggerURL, "/") {
			return nil, fmt.Errorf("invalid SwaggerURL %q: must start with '/'", swaggerURL)
		}
		if swaggerURL == "/" {
			return nil, fmt.Errorf("invalid SwaggerURL %q: must not be '/'", swaggerURL)
		}
		swaggerPattern := swaggerURL
		if !strings.HasSuffix(swaggerPattern, "/") {
			swaggerPattern += "/"
			adminMux.Handle(swaggerURL, http.RedirectHandler(swaggerPattern, http.StatusPermanentRedirect))
		}
		adminMux.Handle(swaggerPattern, http.StripPrefix(swaggerPattern, c.openAPIHandler))
	}
	adminMux.Handle("/", gzipHandler)
	gwServer := &http.Server{
		Addr:    gatewayAddr,
		Handler: adminMux,
	}
	log.Info(ctx, "msg", "Starting HTTP server", "address", gatewayAddr)
	return gwServer, nil
}

func (c *cb) runHTTP(ctx context.Context, svr *http.Server) error {
	// If the peer server already failed (cancelling gctx), exit cleanly
	// so the peer's error is what g.Wait() returns, not context.Canceled.
	if ctx.Err() != nil {
		return nil
	}
	return svr.ListenAndServe()
}

// Native stats/opentelemetry options, built during processConfig().
var (
	otelGRPCOptionsSet bool            // true after processConfig builds or user calls SetOTELOptions
	otelGRPCOptions    grpcotel.Options // value used by getGRPCServerOptions / initHTTP
	otelMeterProvider  *sdkmetric.MeterProvider
	otelUseLegacy      bool            // set from config; forces legacy otelgrpc even if SetOTELOptions was called
)

// Legacy otelgrpc options — only used when OTEL_USE_LEGACY_INSTRUMENTATION=true.
var otelGRPCServerOpts = []otelgrpc.Option{
	otelgrpc.WithFilter(defaultOTELFilter),
}

var otelGRPCClientOpts = []otelgrpc.Option{
	otelgrpc.WithFilter(defaultOTELFilter),
}

func defaultOTELFilter(info *stats.RPCTagInfo) bool {
	return interceptors.FilterMethodsFunc(context.Background(), info.FullMethodName)
}

// Deprecated: Use SetOTELOptions instead. Only applies when
// OTEL_USE_LEGACY_INSTRUMENTATION=true.
func SetOTELGRPCServerOptions(opts ...otelgrpc.Option) {
	otelGRPCServerOpts = opts
}

// Deprecated: Use SetOTELOptions instead. Only applies when
// OTEL_USE_LEGACY_INSTRUMENTATION=true.
func SetOTELGRPCClientOptions(opts ...otelgrpc.Option) {
	otelGRPCClientOpts = opts
}

// SetOTELOptions configures the native gRPC stats/opentelemetry integration.
// Must be called during init, before the gRPC server starts.
// When set, processConfig() will NOT overwrite these with auto-built options.
func SetOTELOptions(opts grpcotel.Options) {
	otelGRPCOptions = opts
	otelGRPCOptionsSet = true
}

// buildOTELOptions constructs grpcotel.Options from the global TracerProvider
// and TextMapPropagator. If SetupOpenTelemetry was not called, the no-op
// defaults from the OTel SDK are used.
func buildOTELOptions() grpcotel.Options {
	opts := grpcotel.Options{
		MetricsOptions: grpcotel.MetricsOptions{
			MethodAttributeFilter: func(method string) bool {
				return interceptors.FilterMethodsFunc(context.Background(), method)
			},
		},
		TraceOptions: experimental.TraceOptions{
			TracerProvider:    otel.GetTracerProvider(),
			TextMapPropagator: otel.GetTextMapPropagator(),
		},
	}
	if otelMeterProvider != nil {
		opts.MetricsOptions.MeterProvider = otelMeterProvider
	}
	return opts
}

func (c *cb) getGRPCServerOptions() []grpc.ServerOption {
	so := make([]grpc.ServerOption, 0)

	// Use native stats/opentelemetry unless legacy mode is forced via config.
	if !otelUseLegacy && otelGRPCOptionsSet {
		so = append(so, grpcotel.ServerOption(otelGRPCOptions))
	} else {
		so = append(so, grpc.StatsHandler(otelgrpc.NewServerHandler(otelGRPCServerOpts...)))
	}
	so = append(so,
		grpc.ChainUnaryInterceptor(interceptors.DefaultInterceptors()...),
		grpc.ChainStreamInterceptor(interceptors.DefaultStreamInterceptors()...),
	)

	// Add message size limits if configured
	if c.config.GRPCMaxRecvMsgSize > 0 {
		so = append(so, grpc.MaxRecvMsgSize(c.config.GRPCMaxRecvMsgSize))
	}

	if c.config.GRPCMaxSendMsgSize > 0 {
		so = append(so, grpc.MaxSendMsgSize(c.config.GRPCMaxSendMsgSize))
	}

	if c.config.GRPCServerMaxConnectionAgeGraceInSeconds > 0 ||
		c.config.GRPCServerMaxConnectionAgeInSeconds > 0 ||
		c.config.GRPCServerMaxConnectionIdleInSeconds > 0 {
		option := keepalive.ServerParameters{}
		if c.config.GRPCServerMaxConnectionIdleInSeconds > 0 {
			option.MaxConnectionIdle = time.Duration(
				c.config.GRPCServerMaxConnectionIdleInSeconds,
			) * time.Second
		}
		if c.config.GRPCServerMaxConnectionAgeInSeconds > 0 {
			option.MaxConnectionAge = time.Duration(
				c.config.GRPCServerMaxConnectionAgeInSeconds,
			) * time.Second
		}
		if c.config.GRPCServerMaxConnectionAgeGraceInSeconds > 0 {
			option.MaxConnectionAgeGrace = time.Duration(
				c.config.GRPCServerMaxConnectionAgeGraceInSeconds,
			) * time.Second
		}
		so = append(so, grpc.KeepaliveParams(option))
	}
	return so
}

func loadTLSCredentials(
	certFile, keyFile string,
	insecureSkipVerify bool,
) (credentials.TransportCredentials, error) {
	// Load server's certificate and private key
	serverCert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}

	// Create the credentials and return it
	config := &tls.Config{
		Certificates:       []tls.Certificate{serverCert},
		ClientAuth:         tls.NoClientCert,
		InsecureSkipVerify: insecureSkipVerify,
	}

	return credentials.NewTLS(config), nil
}

func (c *cb) initGRPC(ctx context.Context) (*grpc.Server, error) {
	so := c.getGRPCServerOptions()
	if c.config.GRPCTLSCertFile != "" && c.config.GRPCTLSKeyFile != "" {
		creds, err := loadTLSCredentials(
			c.config.GRPCTLSCertFile,
			c.config.GRPCTLSKeyFile,
			c.config.GRPCTLSInsecureSkipVerify,
		)
		if err != nil {
			return nil, err
		}
		c.creds = creds
		so = append(so, grpc.Creds(creds))
	}
	grpcServer := grpc.NewServer(so...)
	for _, s := range c.svc {
		if err := s.InitGRPC(ctx, grpcServer); err != nil {
			return nil, err
		}
	}
	return grpcServer, nil
}

func (c *cb) runGRPC(ctx context.Context, svr *grpc.Server, unixLis net.Listener) error {
	// If the peer server already failed (cancelling gctx), exit cleanly
	// so the peer's error is what g.Wait() returns, not context.Canceled.
	if ctx.Err() != nil {
		return nil
	}
	grpcServerEndpoint := fmt.Sprintf("%s:%d", c.config.ListenHost, c.config.GRPCPort)
	lis, err := net.Listen("tcp", grpcServerEndpoint)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}
	if !c.config.DisableGRPCReflection {
		reflection.Register(svr)
	}
	// Start serving on the unix socket in a background goroutine.
	if unixLis != nil {
		go func() {
			if err := svr.Serve(unixLis); err != nil && !errors.Is(err, grpc.ErrServerStopped) {
				log.Error(context.Background(), "msg", "unix socket gRPC server error", "err", err)
			}
		}()
		log.Info(ctx, "msg", "gRPC also listening on unix socket", "path", c.unixSocketPath)
	}
	log.Info(ctx, "msg", "Starting GRPC server", "address", grpcServerEndpoint)
	return svr.Serve(lis)
}

// Run starts the service
// It will block until the service is stopped
// It will return an error if the service fails to start
// It will return nil if the service is stopped
// It will return an error if the service fails to stop
// It will return an error if the service fails to run
func (c *cb) Run() error {
	ctx := context.Background()
	ctx, c.cancelFunc = context.WithCancel(ctx)
	defer c.cancelFunc()

	var err error

	c.grpcServer, err = c.initGRPC(ctx)
	if err != nil {
		return err
	}

	// Create unix socket for the gateway before initHTTP so the endpoint is known.
	var unixLis net.Listener
	if !c.config.DisableUnixGateway {
		tmp, tmpErr := os.CreateTemp("", fmt.Sprintf("coldbrew-%d-*.sock", os.Getpid()))
		if tmpErr != nil {
			log.Warn(ctx, "msg", "failed to allocate unix socket path, falling back to TCP for gateway",
				"err", tmpErr)
		} else {
			socketPath := tmp.Name()
			tmp.Close()
			os.Remove(socketPath) // remove placeholder so net.Listen can create the socket
			unixLis, err = net.Listen("unix", socketPath)
			if err != nil {
				log.Warn(ctx, "msg", "failed to create unix socket, falling back to TCP for gateway",
					"path", socketPath, "err", err)
			} else {
				c.unixSocketPath = socketPath
				c.closers = append(c.closers, closerFunc(func() error {
					return os.Remove(socketPath)
				}))
				log.Info(ctx, "msg", "Unix socket created for gateway", "path", socketPath)
			}
		}
	}

	c.httpServer, err = c.initHTTP(ctx)
	if err != nil {
		// Clean up unix socket listener and file if initHTTP fails.
		if unixLis != nil {
			unixLis.Close()
		}
		c.close()
		return err
	}

	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error {
		err := c.runGRPC(gctx, c.grpcServer, unixLis)
		// Expected shutdown error — don't cancel the group.
		if errors.Is(err, grpc.ErrServerStopped) {
			return nil
		}
		return err
	})
	g.Go(func() error {
		err := c.runHTTP(gctx, c.httpServer)
		// Expected shutdown error — don't cancel the group.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	})
	// When one server exits with an unexpected error (or parent context is
	// cancelled by signal handler), stop the peer so g.Wait() completes.
	g.Go(func() error {
		<-gctx.Done()
		if c.grpcServer != nil {
			c.grpcServer.Stop()
		}
		if c.httpServer != nil {
			c.httpServer.Close()
		}
		return nil
	})
	err = g.Wait()
	c.gracefulWait.Wait() // if graceful shutdown is in progress wait for it to finish
	c.close()
	return err
}

// closerFunc adapts a plain function into an io.Closer.
type closerFunc func() error

func (f closerFunc) Close() error { return f() }
func (closerFunc) String() string { return "closerFunc" }

func (c *cb) close() {
	for _, closer := range c.closers {
		if closer != nil {
			log.Info(context.Background(), "closing", closer)
			err := closer.Close()
			if err != nil {
				log.Error(context.Background(), "msg", "Failed to close resource", "err", err, "resource", closer)
			}
		}
	}
}

// Stop stops the server gracefully
// It will wait for the duration specified in the config for the healthcheck to pass
func (c *cb) Stop(dur time.Duration) error {
	c.gracefulWait.Add(1) // tell runner that a graceful shutdow is in progress
	defer c.gracefulWait.Done()
	ctx, cancel := context.WithTimeout(context.Background(), dur)
	defer func() {
		cancel()
		if c.cancelFunc != nil {
			c.cancelFunc()
		}
	}()

	for _, svc := range c.svc {
		if s, ok := svc.(CBGracefulStopper); ok {
			s.FailCheck(true)
		}
	}
	if c.config.HealthcheckWaitDurationInSeconds > 0 {
		d := time.Second * time.Duration(c.config.HealthcheckWaitDurationInSeconds)
		log.Info(context.Background(), "msg", "graceful shutdown timer started", "duration", d)
		time.Sleep(d)
		log.Info(context.Background(), "msg", "graceful shutdown timer finished", "duration", d)
	}
	log.Info(context.Background(), "msg", "Server shut down started, bye bye")
	if c.httpServer != nil {
		if err := c.httpServer.Shutdown(ctx); err != nil {
			log.Error(context.Background(), "msg", "http server shutdown error", "err", err)
		}
	}
	if c.grpcServer != nil {
		timedCall(ctx, c.grpcServer.GracefulStop)
		c.grpcServer.Stop()
	}
	for _, svc := range c.svc {
		// call stopper to stop services
		if s, ok := svc.(CBStopper); ok {
			s.Stop()
		}
	}
	return nil
}

func timedCall(ctx context.Context, f func()) {
	done := make(chan struct{})
	go func() {
		f()
		close(done)
	}()

	select {
	case <-done:
		log.Info(context.Background(), "grpc graceful shutdown complete")
	case <-ctx.Done():
		log.Info(context.Background(), "grpc graceful shutdown failed, forcing shutdown")
	}
}

// New creates a new ColdBrew object
// It takes a config object and returns a CB interface
// The CB interface is used to start and stop the server
// The CB interface also provides a way to add services to the server
// The services are added using the AddService method
// The services are started and stopped in the order they are added
func New(c config.Config) CB {
	impl := &cb{
		config: c,
		svc:    make([]CBService, 0),
	}
	impl.processConfig()
	// Log validation warnings after processConfig so the logger is configured
	if warnings := impl.config.Validate(); len(warnings) > 0 {
		for _, w := range warnings {
			log.Warn(context.Background(), "msg", "config validation warning", "warning", w)
		}
	}
	return impl
}
