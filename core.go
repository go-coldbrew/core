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
	feature_flags "github.com/go-coldbrew/feature-flags"
	"github.com/go-coldbrew/interceptors"
	"github.com/go-coldbrew/log"
	grpc_opentracing "github.com/grpc-ecosystem/go-grpc-middleware/tracing/opentracing"
	grpc_prometheus "github.com/grpc-ecosystem/go-grpc-prometheus"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/opentracing/opentracing-go"
	"github.com/opentracing/opentracing-go/ext"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"
)

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
		return errors.New("service is nil")
	}
	c.svc = append(c.svc, svc)
	return nil
}

func (c *cb) SetOpenAPIHandler(handler http.Handler) {
	c.openAPIHandler = handler
}

func (c *cb) processConfig() {
	setupLogger(c.config.LogLevel, c.config.JSONLogs)
	setupNewRelic(c.config.AppName, c.config.NewRelicLicenseKey)
	setupSentry(c.config.SentryDSN)
	setupEnvironment(c.config.Environment)
	setupReleaseName(c.config.ReleaseName)
	cls := setupJaeger(c.config.AppName)
	if cls != nil {
		c.closers = append(c.closers, cls)
	}
	setupHystrix()
	configureInterceptors(c.config.DoNotLogGRPCReflection, c.config.TraceHeaderName)
	if !c.config.DisableSignalHandler {
		dur := time.Second * 10
		if c.config.ShutdownDurationInSeconds > 0 {
			dur = time.Second * time.Duration(c.config.ShutdownDurationInSeconds)
		}
		startSignalHandler(c, dur)
	}
	if c.config.EnablePrometheusGRPCHistogram {
		grpc_prometheus.EnableHandlingTimeHistogram()
	}
}

// https://grpc-ecosystem.github.io/grpc-gateway/docs/operations/tracing/#opentracing-support
var grpcGatewayTag = opentracing.Tag{Key: string(ext.Component), Value: "grpc-gateway"}

func tracingWrapper(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parentSpanContext, err := opentracing.GlobalTracer().Extract(
			opentracing.HTTPHeaders,
			opentracing.HTTPHeadersCarrier(r.Header))
		if err == nil || err == opentracing.ErrSpanContextNotFound {
			if interceptors.FilterMethodsFunc(r.Context(), r.URL.Path) {
				serverSpan := opentracing.GlobalTracer().StartSpan(
					"ServeHTTP",
					// this is magical, it attaches the new span to the parent parentSpanContext, and creates an unparented one if empty.
					ext.RPCServerOption(parentSpanContext),
					grpcGatewayTag,
					opentracing.Tag{Key: string(ext.HTTPUrl), Value: r.URL.Path},
					opentracing.Tag{Key: string(ext.HTTPMethod), Value: r.Method},
				)
				r = r.WithContext(opentracing.ContextWithSpan(r.Context(), serverSpan))
				defer serverSpan.Finish()
			}
		}
		_, han := interceptors.NRHttpTracer("", h.ServeHTTP)
		han(w, r)
	})
}

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
	}

	if c.config.UseJSONBuiltinMarshaller {
		muxOpts = append(muxOpts, runtime.WithMarshalerOption(c.config.JSONBuiltinMarshallerMime, &runtime.JSONBuiltin{}))
	}

	mux := runtime.NewServeMux(muxOpts...)

	opts := []grpc.DialOption{grpc.WithInsecure(),
		grpc.WithUnaryInterceptor(
			interceptors.DefaultClientInterceptor(
				grpc_opentracing.WithTraceHeaderName(c.config.TraceHeaderName),
				grpc_opentracing.WithFilterFunc(interceptors.FilterMethodsFunc),
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
			if !c.config.DisableSwagger && c.openAPIHandler != nil && strings.HasPrefix(r.URL.Path, "/swagger/") {
				http.StripPrefix("/swagger/", c.openAPIHandler).ServeHTTP(w, r)
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
	log.Info(ctx, "Starting HTTP server on ", gatewayAddr)
	return gwServer, nil
}

func (c *cb) runHTTP(ctx context.Context, svr *http.Server) error {
	return svr.ListenAndServe()
}

func (c *cb) getGRPCServerOptions() []grpc.ServerOption {
	so := make([]grpc.ServerOption, 0, 0)
	so = append(so,
		grpc.ChainUnaryInterceptor(interceptors.DefaultInterceptors()...),
		grpc.ChainStreamInterceptor(interceptors.DefaultStreamInterceptors()...),
	)
	return so
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
	log.Info(ctx, "Starting GRPC server on ", grpcServerEndpoint)
	return svr.Serve(lis)
}

func (c *cb) Run() error {
	ctx := context.Background()
	ctx, c.cancelFunc = context.WithCancel(ctx)
	defer c.cancelFunc()

	var err error

	err = feature_flags.Initialize(c.config.AppName, c.config.FeatureFlagConfig)
	if err != nil {
		return err
	}

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
	if c.httpServer != nil {
		go c.httpServer.Shutdown(ctx)
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

//New creates a new ColdBrew object
func New(c config.Config) CB {
	impl := &cb{
		config: c,
		svc:    make([]CBService, 0, 0),
	}
	impl.processConfig()
	return impl
}
