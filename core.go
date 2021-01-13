package core

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"

	"github.com/go-coldbrew/core/config"
	"github.com/go-coldbrew/interceptors"
	"github.com/go-coldbrew/log"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

type cb struct {
	svc            []CBService
	openAPIHandler http.Handler
	config         config.Config
}

func (c *cb) SetService(svc CBService) error {
	c.svc = append(c.svc, svc)
	return nil
}

func (c *cb) SetOpenAPIHandler(handler http.Handler) {
	c.openAPIHandler = handler
}

func (c *cb) processConfig() {
	setupNewRelic(c.config.AppName, c.config.NewRelicLicenseKey)
	setupSentry(c.config.SentryDSN)
}

func (c *cb) initHTTP(ctx context.Context) (*http.Server, error) {
	// Register gRPC server endpoint
	// Note: Make sure the gRPC server is running properly and accessible
	grpcServerEndpoint := fmt.Sprintf("%s:%d", c.config.ListenHost, c.config.GRPCPort)
	mux := runtime.NewServeMux()
	opts := []grpc.DialOption{grpc.WithInsecure()}
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
			} else if !c.config.DisableDebug && strings.HasPrefix(r.URL.Path, "/debug/") {
				pprof.Index(w, r)
				return
			} else if !c.config.DisablePormetheus && strings.HasPrefix(r.URL.Path, "/metrics") {
				promhttp.Handler().ServeHTTP(w, r)
				return
			}
			mux.ServeHTTP(w, r)
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
	log.Info(ctx, "Starting GRPC server on ", grpcServerEndpoint)
	return svr.Serve(lis)
}

func (c *cb) Run() error {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	grpcSvr, err := c.initGRPC(ctx)
	if err != nil {
		return err
	}

	httpSvr, err := c.initHTTP(ctx)
	if err != nil {
		return err
	}

	errChan := make(chan error, 0)
	go func() {
		errChan <- c.runGRPC(ctx, grpcSvr)
	}()
	go func() {
		errChan <- c.runHTTP(ctx, httpSvr)
	}()
	return <-errChan
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
