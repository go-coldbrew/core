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
	"strconv"
	"strings"
	"sync"
	"time"

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
	semconv "go.opentelemetry.io/otel/semconv/v1.40.0"
	oteltrace "go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/stats"
	"google.golang.org/grpc/credentials/insecure"
	_ "google.golang.org/grpc/encoding/gzip"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
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
	ConfigureInterceptors(c.config.DoNotLogGRPCReflection, c.config.TraceHeaderName)
	if !c.config.DisableSignalHandler {
		dur := time.Second * 10
		if c.config.ShutdownDurationInSeconds > 0 {
			dur = time.Second * time.Duration(c.config.ShutdownDurationInSeconds)
		}
		startSignalHandler(c, dur)
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

	// Setup OpenTelemetry - custom OTLP takes precedence over New Relic
	if c.config.OTLPEndpoint != "" {
		// Use custom OTLP configuration
		headers := parseHeaders(c.config.OTLPHeaders)
		otlpConfig := OTLPConfig{
			Endpoint:             c.config.OTLPEndpoint,
			Headers:              headers,
			ServiceName:          c.config.AppName,
			ServiceVersion:       c.config.ReleaseName,
			SamplingRatio:        c.config.OTLPSamplingRatio,
			Compression:          c.config.OTLPCompression,
			UseOpenTracingBridge: c.config.OTLPUseOpenTracingBridge, //nolint:staticcheck // reading deprecated field for backward compat
			Insecure:             c.config.OTLPInsecure,
		}
		if err := SetupOpenTelemetry(otlpConfig); err != nil {
			log.Error(context.Background(), "msg", "Failed to setup custom OTLP", "err", err)
		}
	} else if c.config.NewRelicOpentelemetry {
		// Fall back to New Relic OpenTelemetry if no custom OTLP is configured
		err := SetupNROpenTelemetry(
			nrName,
			c.config.NewRelicLicenseKey,
			c.config.ReleaseName,
			c.config.NewRelicOpentelemetrySample,
		)
		if err != nil {
			log.Error(context.Background(), "msg", "Failed to setup New Relic OpenTelemetry", "err", err)
		}
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
	attrs := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(r.Method),
		semconv.URLPath(r.URL.Path),
	}
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
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			w = rec
			defer func() {
				if recovered := recover(); recovered != nil {
					if !rec.wroteHeader {
						rec.status = http.StatusInternalServerError
					}
					serverSpan.RecordError(fmt.Errorf("panic: %v", recovered))
					endSpan(serverSpan, rec)
					panic(recovered)
				}
				endSpan(serverSpan, rec)
			}()
		}

		_, han := interceptors.NRHttpTracer("", h.ServeHTTP)
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

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(creds),
		grpc.WithStatsHandler(otelgrpc.NewClientHandler(otelGRPCClientOpts...)),
		grpc.WithUnaryInterceptor(
			interceptors.DefaultClientInterceptor(
				interceptors.WithoutHystrix(),
			),
		),
	}
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
		if err := s.InitHTTP(ctx, mux, grpcServerEndpoint, opts); err != nil {
			return nil, err
		}
	}

	// Start HTTP server (and proxy calls to gRPC server endpoint)
	gatewayAddr := fmt.Sprintf("%s:%d", c.config.ListenHost, c.config.HTTPPort)
	promHandler := promhttp.Handler()
	gzipHandler := gzhttp.GzipHandler(tracingWrapper(mux))
	gwServer := &http.Server{
		Addr: gatewayAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !c.config.DisableSwagger && c.openAPIHandler != nil &&
				strings.HasPrefix(r.URL.Path, c.config.SwaggerURL) {
				http.StripPrefix(c.config.SwaggerURL, c.openAPIHandler).ServeHTTP(w, r)
				return
			} else if !c.config.DisableDebug && strings.HasPrefix(r.URL.Path, "/debug/pprof/cmdline") {
				pprof.Cmdline(w, r)
				return
			} else if !c.config.DisableDebug && strings.HasPrefix(r.URL.Path, "/debug/pprof/profile") {
				pprof.Profile(w, r)
				return
			} else if !c.config.DisableDebug && strings.HasPrefix(r.URL.Path, "/debug/pprof/symbol") {
				pprof.Symbol(w, r)
				return
			} else if !c.config.DisableDebug && strings.HasPrefix(r.URL.Path, "/debug/pprof/trace") {
				pprof.Trace(w, r)
				return
			} else if !c.config.DisableDebug && strings.HasPrefix(r.URL.Path, "/debug/pprof/") {
				pprof.Index(w, r)
				return
			} else if !(c.config.DisablePrometheus || c.config.DisablePormetheus) && strings.HasPrefix(r.URL.Path, "/metrics") { //nolint:staticcheck // intentional use of deprecated field for backward compatibility
				promHandler.ServeHTTP(w, r)
				return
			}
			gzipHandler.ServeHTTP(w, r)
		}),
	}
	log.Info(ctx, "msg", "Starting HTTP server", "address", gatewayAddr)
	return gwServer, nil
}

func (c *cb) runHTTP(_ context.Context, svr *http.Server) error {
	return svr.ListenAndServe()
}

// otelgrpc options configured during init via SetOTELGRPCServerOptions/SetOTELGRPCClientOptions.
// Defaults filter out health/ready/reflection RPCs to reduce noise.
var otelGRPCServerOpts = []otelgrpc.Option{
	otelgrpc.WithFilter(defaultOTELFilter),
}
var otelGRPCClientOpts = []otelgrpc.Option{
	otelgrpc.WithFilter(defaultOTELFilter),
}

// defaultOTELFilter excludes health checks, readiness probes, and gRPC
// reflection from tracing to reduce noise — matching the previous
// grpc_opentracing filter behavior.
func defaultOTELFilter(info *stats.RPCTagInfo) bool {
	return interceptors.FilterMethodsFunc(context.Background(), info.FullMethodName)
}

// SetOTELGRPCServerOptions sets options for the OTEL gRPC server stats handler.
// Must be called during init, before the gRPC server starts.
// Example: core.SetOTELGRPCServerOptions(otelgrpc.WithFilter(...))
func SetOTELGRPCServerOptions(opts ...otelgrpc.Option) {
	otelGRPCServerOpts = opts
}

// SetOTELGRPCClientOptions sets options for the OTEL gRPC client stats handler.
// Must be called during init, before the gRPC client is created.
func SetOTELGRPCClientOptions(opts ...otelgrpc.Option) {
	otelGRPCClientOpts = opts
}

func (c *cb) getGRPCServerOptions() []grpc.ServerOption {
	so := make([]grpc.ServerOption, 0)
	so = append(so,
		grpc.StatsHandler(otelgrpc.NewServerHandler(otelGRPCServerOpts...)),
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

func (c *cb) runGRPC(ctx context.Context, svr *grpc.Server) error {
	grpcServerEndpoint := fmt.Sprintf("%s:%d", c.config.ListenHost, c.config.GRPCPort)
	lis, err := net.Listen("tcp", grpcServerEndpoint)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}
	if !c.config.DisableGRPCReflection {
		reflection.Register(svr)
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

	c.httpServer, err = c.initHTTP(ctx)
	if err != nil {
		return err
	}

	errChan := make(chan error, 2)
	go func() {
		errChan <- c.runGRPC(ctx, c.grpcServer)
	}()
	go func() {
		errChan <- c.runHTTP(ctx, c.httpServer)
	}()
	err = <-errChan
	// Stop the peer server only on unexpected failures to avoid racing
	// with an in-progress graceful shutdown.
	if err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, grpc.ErrServerStopped) {
		if c.grpcServer != nil {
			c.grpcServer.Stop()
		}
		if c.httpServer != nil {
			c.httpServer.Close()
		}
	}
	c.gracefulWait.Wait() // if graceful shutdown is in progress wait for it to finish
	c.close()
	return err
}

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
