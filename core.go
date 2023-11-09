package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"
	"sync"
	"time"

	"github.com/NYTimes/gziphandler"
	"github.com/go-coldbrew/core/config"
	"github.com/go-coldbrew/interceptors"
	"github.com/go-coldbrew/log"
	"github.com/go-coldbrew/log/loggers"
	"github.com/go-coldbrew/options"
	grpcOpentracing "github.com/grpc-ecosystem/go-grpc-middleware/tracing/opentracing"
	grpcPrometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
)

const (
	// DefaultShutdownDurationInSeconds is the default shutdown duration in seconds.
	DefaultShutdownDurationInSeconds = 15
)

// ErrNoService is returned when no service is set.
var ErrNoService = errors.New("no service is set")

// CB should be implemented by the service.
var _ CB = (*cb)(nil)

type cb struct {
	svc            []CBService
	openAPIHandler http.Handler
	config         config.Config
	closers        []io.Closer
	grpcServer     *grpc.Server
	httpServer     *http.Server
	cancelFunc     context.CancelFunc
	gracefulWait   sync.WaitGroup
}

func (c *cb) SetService(svc CBService) error {
	if svc == nil {
		return ErrNoService
	}

	c.svc = append(c.svc, svc)

	return nil
}

// SetOpenAPIHandler sets the openapi handler
// This is used to serve the openapi spec
// This is optional.
func (c *cb) SetOpenAPIHandler(handler http.Handler) {
	c.openAPIHandler = handler
}

// processConfig processes the config and sets up the logger, newrelic, sentry,
// environment, release name, jaeger, hystrix prometheus and signal handler.
func (c *cb) processConfig() {
	ctx := context.Background()
	err := SetupLogger(c.config.LogLevel, c.config.JSONLogs)
	if err != nil {
		log.Error(ctx, "msg", "Error setting up logger", "err", err)
	}

	nrName := c.config.AppName
	if nrName == "" {
		nrName = c.config.AppName
	}

	err = SetupNewRelic(nrName, c.config.NewRelicLicenseKey, c.config.NewRelicDistributedTracing)
	if err != nil {
		log.Error(ctx, "msg", "Error setting up New Relic", "err", err)
	}

	if !c.config.DisableAutoMaxProcs {
		SetupAutoMaxProcs()
	}

	SetupSentry(c.config.SentryDSN)
	SetupEnvironment(c.config.Environment)
	SetupReleaseName(c.config.ReleaseName)
	SetupHystrixPrometheus()
	ConfigureInterceptors(c.config.DoNotLogGRPCReflection, c.config.TraceHeaderName)

	cls := setupJaeger(c.config.AppName)
	if cls != nil {
		c.closers = append(c.closers, cls)
	}

	if !c.config.DisableSignalHandler {
		dur := time.Second * time.Duration(c.config.ShutdownDurationInSeconds)
		if c.config.ShutdownDurationInSeconds <= 0 {
			dur = time.Second * DefaultShutdownDurationInSeconds
		}

		startSignalHandler(c, dur)
	}

	if c.config.EnablePrometheusGRPCHistogram {
		grpcPrometheus.EnableHandlingTimeHistogram()
	}

	if c.config.NewRelicOpentelemetry {
		err := SetupNROpenTelemetry(nrName, c.config.NewRelicLicenseKey, c.config.ReleaseName, c.config.NewRelicOpentelemetrySample)
		if err != nil {
			log.Error(ctx, "msg", "Error setting up New Relic OpenTelemetry", "err", err)
		}
	}
}

// https://grpc-ecosystem.github.io/grpc-gateway/docs/operations/tracing/#opentracing-support
var grpcGatewayTag = opentracing.Tag{Key: string(ext.Component), Value: "grpc-gateway"}

// tracingWrapper is a middleware that creates a new span for each incoming request.
// It also adds the span to the context so it can be used by other middlewares or handlers to add additional tags.
func tracingWrapper(handler http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, req *http.Request) {
		parentSpanContext, err := opentracing.GlobalTracer().Extract(
			opentracing.HTTPHeaders,
			opentracing.HTTPHeadersCarrier(req.Header))
		if err == nil || err == opentracing.ErrSpanContextNotFound {
			if interceptors.FilterMethodsFunc(req.Context(), req.URL.Path) {
				serverSpan := opentracing.GlobalTracer().StartSpan(
					"ServeHTTP",
					// this is magical, it attaches the new span to the parent parentSpanContext,
					// and creates an unparented one if empty.
					ext.RPCServerOption(parentSpanContext),
					grpcGatewayTag,
					opentracing.Tag{Key: string(ext.HTTPUrl), Value: req.URL.Path},
					opentracing.Tag{Key: string(ext.HTTPMethod), Value: req.Method},
				)
				req = req.WithContext(opentracing.ContextWithSpan(req.Context(), serverSpan))
				defer serverSpan.Finish()
			}
		}
		_, han := interceptors.NRHttpTracer("", handler.ServeHTTP)
		// add this info to log
		ctx := req.Context()
		ctx = options.AddToOptions(ctx, "", "")
		ctx = loggers.AddToLogContext(ctx, "httpPath", req.URL.Path)
		req = req.WithContext(ctx)
		han(writer, req)
	})
}

// getCustomHeaderMatcher returns a matcher that matches the given header and prefix.
func getCustomHeaderMatcher(prefix, header string) func(string) (string, bool) {
	prefix = strings.ToLower(prefix)
	header = strings.ToLower(header)

	return func(key string) (string, bool) {
		key = strings.ToLower(key)
		if key == header {
			return key, true
		} else if len(prefix) > 0 && strings.HasPrefix(key, prefix) {
			return key, true
		}

		return runtime.DefaultHeaderMatcher(key)
	}
}

func (c *cb) initHTTP(ctx context.Context) (*http.Server, error) {
	// Register gRPC server endpoint
	// Note: Make sure the gRPC server is running properly and accessible
	grpcServerEndpoint := fmt.Sprintf("%s:%d", c.config.ListenHost, c.config.GRPCPort)

	pMar := &runtime.ProtoMarshaller{}
	muxOpts := []runtime.ServeMuxOption{
		runtime.WithIncomingHeaderMatcher(getCustomHeaderMatcher(c.config.HTTPHeaderPrefix, c.config.TraceHeaderName)),
		runtime.WithMarshalerOption("application/proto", pMar),
		runtime.WithMarshalerOption("application/protobuf", pMar),
		runtime.WithMetadata(func(ctx context.Context, r *http.Request) metadata.MD {
			meta := make(map[string]string)
			if method, ok := runtime.RPCMethod(ctx); ok {
				meta["method"] = method
			}
			if pattern, ok := runtime.HTTPPathPattern(ctx); ok {
				meta["pattern"] = pattern
			}

			return metadata.New(meta)
		}),
	}

	if c.config.UseJSONBuiltinMarshaller {
		muxOpts = append(muxOpts, runtime.WithMarshalerOption(c.config.JSONBuiltinMarshallerMime, &runtime.JSONBuiltin{}))
	}

	mux := runtime.NewServeMux(muxOpts...)

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithUnaryInterceptor(
			interceptors.DefaultClientInterceptor(
				grpcOpentracing.WithTraceHeaderName(c.config.TraceHeaderName),
				grpcOpentracing.WithFilterFunc(interceptors.FilterMethodsFunc),
				interceptors.WithoutHystrix(),
			),
		),
	}
	for _, s := range c.svc {
		if err := s.InitHTTP(ctx, mux, grpcServerEndpoint, opts); err != nil {
			return nil, err
		}
	}

	// Start HTTP server (and proxy calls to gRPC server endpoint)
	gatewayAddr := fmt.Sprintf("%s:%d", c.config.ListenHost, c.config.HTTPPort)
	gwServer := &http.Server{
		Addr: gatewayAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !c.config.DisableSwagger && c.openAPIHandler != nil && strings.HasPrefix(r.URL.Path, c.config.SwaggerURL) {
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
			} else if !c.config.DisablePormetheus && strings.HasPrefix(r.URL.Path, "/metrics") {
				promhttp.Handler().ServeHTTP(w, r)

				return
			}
			gziphandler.GzipHandler(tracingWrapper(mux)).ServeHTTP(w, r)
		}),
	}

	log.Info(ctx, "msg", "Starting HTTP server", "address", gatewayAddr)
	return gwServer, nil
}

func (c *cb) runHTTP(_ context.Context, svr *http.Server) error {
	return svr.ListenAndServe()
}

func (c *cb) getGRPCServerOptions() []grpc.ServerOption {
	serverOptions := make([]grpc.ServerOption, 0)
	serverOptions = append(serverOptions,
		grpc.ChainUnaryInterceptor(interceptors.DefaultInterceptors()...),
		grpc.ChainStreamInterceptor(interceptors.DefaultStreamInterceptors()...),
	)

	if c.config.GRPCServerMaxConnectionAgeGraceInSeconds > 0 ||
		c.config.GRPCServerMaxConnectionAgeInSeconds > 0 ||
		c.config.GRPCServerMaxConnectionIdleInSeconds > 0 {
		option := keepalive.ServerParameters{}
		if c.config.GRPCServerMaxConnectionIdleInSeconds > 0 {
			option.MaxConnectionIdle = time.Duration(c.config.GRPCServerMaxConnectionIdleInSeconds) * time.Second
		}

		if c.config.GRPCServerMaxConnectionAgeInSeconds > 0 {
			option.MaxConnectionAge = time.Duration(c.config.GRPCServerMaxConnectionAgeInSeconds) * time.Second
		}

		if c.config.GRPCServerMaxConnectionAgeGraceInSeconds > 0 {
			option.MaxConnectionAgeGrace = time.Duration(c.config.GRPCServerMaxConnectionAgeGraceInSeconds) * time.Second
		}

		serverOptions = append(serverOptions, grpc.KeepaliveParams(option))
	}

	return serverOptions
}

func (c *cb) initGRPC(ctx context.Context) (*grpc.Server, error) {
	grpcServer := grpc.NewServer(c.getGRPCServerOptions()...)
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
// It will return an error if the service fails to run.
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

	c.gracefulWait.Wait() // if graceful shutdown is in progress wait for it to finish
	c.close()

	return err
}

func (c *cb) close() {
	for _, closer := range c.closers {
		if closer != nil {
			log.Info(context.Background(), "closing", closer)
			closer.Close()
		}
	}
}

// Stop stops the server gracefully
// It will wait for the duration specified in the config for the healthcheck to pass.
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

	log.Info(context.Background(), "msg", "Server shut down started, bye")

	if c.httpServer != nil {
		go func(ctx context.Context) {
			err := c.httpServer.Shutdown(ctx)
			if err != nil {
				log.Error(ctx, "msg", "http server shutdown failed", "err", err)
			}
		}(ctx)
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
		log.Info(ctx, "grpc graceful shutdown complete")
	case <-ctx.Done():
		log.Info(ctx, "grpc graceful shutdown failed, forcing shutdown")
	}
}

// New creates a new ColdBrew object
// It takes a config object and returns a CB interface
// The CB interface is used to start and stop the server
// The CB interface also provides a way to add services to the server
// The services are added using the AddService method
// The services are started and stopped in the order they are added.
func New(c config.Config) *cb {
	impl := &cb{ // nolint:exhaustivestruct
		config: c,
		svc:    make([]CBService, 0),
	}
	impl.processConfig()

	return impl
}
