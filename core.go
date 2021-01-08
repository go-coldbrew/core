package core

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"strings"

	"github.com/go-coldbrew/core/config"
	"github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"google.golang.org/grpc"
)

type cb struct {
	svc    CBService
	config config.Config
}

var (
	ErrServiceAlreadyInitialized = errors.New("service is already initialized")
)

func (c *cb) SetService(svc CBService) error {
	if c.svc != nil {
		return ErrServiceAlreadyInitialized
	}
	c.svc = svc
	return nil

}

func (c *cb) init() {

}

func (c *cb) runHTTP(ctx context.Context) error {
	// Register gRPC server endpoint
	// Note: Make sure the gRPC server is running properly and accessible
	grpcServerEndpoint := fmt.Sprintf("0.0.0.0:%d", c.config.GRPCPort)
	mux := runtime.NewServeMux()
	opts := []grpc.DialOption{grpc.WithInsecure()}
	err := c.svc.InitHTTP(ctx, mux, grpcServerEndpoint, opts)
	if err != nil {
		return err
	}

	// Start HTTP server (and proxy calls to gRPC server endpoint)
	gatewayAddr := fmt.Sprintf("0.0.0.0:%d", c.config.HTTPPort)
	gwServer := &http.Server{
		Addr: gatewayAddr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/swagger/") {
				http.StripPrefix("/swagger/", c.svc.GetOpenAPIHandler(ctx)).ServeHTTP(w, r)
				return
			} else if strings.HasPrefix(r.URL.Path, "/debug/") {
				pprof.Index(w, r)
				return
			} else if strings.HasPrefix(r.URL.Path, "/metrics") {
				promhttp.Handler().ServeHTTP(w, r)
				return
			}
			mux.ServeHTTP(w, r)
		}),
	}
	//log.Info("Starting HTTP server on ", gatewayAddr)
	return gwServer.ListenAndServe()
}

func (c *cb) getGRPCServerOptions() []grpc.ServerOption {
	return []grpc.ServerOption{}
}

func (c *cb) runGRPC(ctx context.Context) error {
	grpcServerEndpoint := fmt.Sprintf("0.0.0.0:%d", c.config.GRPCPort)
	lis, err := net.Listen("tcp", grpcServerEndpoint)
	if err != nil {
		return fmt.Errorf("failed to listen: %v", err)
	}
	//log.Info("Starting GRPC server on ", grpcServerEndpoint)
	grpcServer := grpc.NewServer(c.getGRPCServerOptions()...)
	c.svc.InitGRPC(ctx, grpcServer)
	return grpcServer.Serve(lis)
}

func (c *cb) Run() error {
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	errChan := make(chan error, 0)
	go func() {
		errChan <- c.runHTTP(ctx)
	}()
	go func() {
		errChan <- c.runGRPC(ctx)
	}()
	return <-errChan
}

//New creates a new ColdBrew object
func New(c config.Config) CB {
	return &cb{
		config: c,
	}
}
