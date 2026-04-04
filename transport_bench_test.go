package core

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// echoService is a minimal gRPC service for benchmarking transports.
// It uses the raw grpc.ServiceDesc API to avoid needing proto codegen.
type echoService struct{}

func (e *echoService) Echo(ctx context.Context, req *EchoRequest) (*EchoResponse, error) {
	return &EchoResponse{Message: req.Message}, nil
}

// EchoRequest / EchoResponse are simple proto-like structs that implement
// the proto.Message interface minimally via ProtoReflect stubs.
// We use manual marshal/unmarshal to keep the benchmark self-contained.
type EchoRequest struct {
	Message string
}

func (r *EchoRequest) Reset()         {}
func (r *EchoRequest) String() string { return r.Message }
func (r *EchoRequest) ProtoMessage()  {}

type EchoResponse struct {
	Message string
}

func (r *EchoResponse) Reset()         {}
func (r *EchoResponse) String() string { return r.Message }
func (r *EchoResponse) ProtoMessage()  {}

var echoServiceDesc = grpc.ServiceDesc{
	ServiceName: "bench.Echo",
	HandlerType: (*echoServiceInterface)(nil),
	Methods: []grpc.MethodDesc{
		{
			MethodName: "Echo",
			Handler:    echoHandler,
		},
	},
	Streams:  []grpc.StreamDesc{},
	Metadata: "bench.proto",
}

type echoServiceInterface interface {
	Echo(context.Context, *EchoRequest) (*EchoResponse, error)
}

func echoHandler(srv interface{}, ctx context.Context, dec func(interface{}) error, interceptor grpc.UnaryServerInterceptor) (interface{}, error) {
	req := &EchoRequest{}
	if err := dec(req); err != nil {
		return nil, err
	}
	if interceptor == nil {
		return srv.(echoServiceInterface).Echo(ctx, req)
	}
	info := &grpc.UnaryServerInfo{
		Server:     srv,
		FullMethod: "/bench.Echo/Echo",
	}
	handler := func(ctx context.Context, r interface{}) (interface{}, error) {
		return srv.(echoServiceInterface).Echo(ctx, r.(*EchoRequest))
	}
	return interceptor(ctx, req, info, handler)
}

// echoClient invokes the Echo method on a gRPC connection.
func echoClient(ctx context.Context, cc *grpc.ClientConn, msg string) (*EchoResponse, error) {
	resp := &EchoResponse{}
	err := cc.Invoke(ctx, "/bench.Echo/Echo", &EchoRequest{Message: msg}, resp)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

// setupServer creates a gRPC server with the echo service registered.
func setupServer() *grpc.Server {
	s := grpc.NewServer()
	s.RegisterService(&echoServiceDesc, &echoService{})
	return s
}

// BenchmarkTransport_TCP benchmarks gRPC over localhost TCP.
func BenchmarkTransport_TCP(b *testing.B) {
	s := setupServer()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go s.Serve(lis)
	b.Cleanup(func() { s.Stop() })

	cc, err := grpc.NewClient(
		lis.Addr().String(),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { cc.Close() })

	ctx := context.Background()
	// Warm up the connection.
	if _, err := echoClient(ctx, cc, "warmup"); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := echoClient(ctx, cc, "hello"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTransport_Unix benchmarks gRPC over a Unix domain socket.
func BenchmarkTransport_Unix(b *testing.B) {
	s := setupServer()
	sock := b.TempDir() + "/bench.sock"
	lis, err := net.Listen("unix", sock)
	if err != nil {
		b.Fatal(err)
	}
	go s.Serve(lis)
	b.Cleanup(func() { s.Stop() })

	cc, err := grpc.NewClient(
		"unix:"+sock,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { cc.Close() })

	ctx := context.Background()
	if _, err := echoClient(ctx, cc, "warmup"); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := echoClient(ctx, cc, "hello"); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkTransport_Bufconn benchmarks gRPC over an in-memory bufconn.
func BenchmarkTransport_Bufconn(b *testing.B) {
	s := setupServer()
	lis := bufconn.Listen(1024 * 1024)
	go s.Serve(lis)
	b.Cleanup(func() { s.Stop() })

	cc, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return lis.DialContext(ctx)
		}),
	)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { cc.Close() })

	ctx := context.Background()
	if _, err := echoClient(ctx, cc, "warmup"); err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		if _, err := echoClient(ctx, cc, "hello"); err != nil {
			b.Fatal(err)
		}
	}
}
