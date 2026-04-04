package benchmarks

import (
	"context"
	"fmt"
	"net"
	"os"
	"testing"

	"github.com/go-coldbrew/core/benchmarks/benchpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

// benchEchoServer implements the generated BenchService gRPC server.
type benchEchoServer struct {
	benchpb.UnimplementedBenchServiceServer
}

func (s *benchEchoServer) Echo(_ context.Context, req *benchpb.BenchRequest) (*benchpb.BenchResponse, error) {
	return &benchpb.BenchResponse{
		Items:      req.Items,
		TotalCount: int64(len(req.Items)),
		RequestId:  "bench-req-id",
	}, nil
}

func setupBenchServer() *grpc.Server {
	s := grpc.NewServer()
	benchpb.RegisterBenchServiceServer(s, &benchEchoServer{})
	return s
}

func benchClient(ctx context.Context, cc *grpc.ClientConn, req *benchpb.BenchRequest) (*benchpb.BenchResponse, error) {
	return benchpb.NewBenchServiceClient(cc).Echo(ctx, req)
}

// --- TCP vs Unix vs Bufconn (using default proto codec) ---

func BenchmarkTransport_TCP(b *testing.B) {
	s := setupBenchServer()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.Fatal(err)
	}
	go s.Serve(lis)
	b.Cleanup(func() { s.Stop() })

	cc, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { cc.Close() })

	ctx := context.Background()
	if _, err := benchClient(ctx, cc, &benchpb.BenchRequest{Items: makeItems(1)}); err != nil {
		b.Fatal(err)
	}

	for _, pc := range payloadConfigs {
		req := &benchpb.BenchRequest{Items: makeItems(pc.items)}
		b.Run(pc.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := benchClient(ctx, cc, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkTransport_Unix(b *testing.B) {
	s := setupBenchServer()
	sock := fmt.Sprintf("/tmp/bench-transport-%d.sock", os.Getpid())
	os.Remove(sock)
	b.Cleanup(func() { os.Remove(sock) })

	lis, err := net.Listen("unix", sock)
	if err != nil {
		b.Fatal(err)
	}
	go s.Serve(lis)
	b.Cleanup(func() { s.Stop() })

	cc, err := grpc.NewClient("unix:"+sock, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { cc.Close() })

	ctx := context.Background()
	if _, err := benchClient(ctx, cc, &benchpb.BenchRequest{Items: makeItems(1)}); err != nil {
		b.Fatal(err)
	}

	for _, pc := range payloadConfigs {
		req := &benchpb.BenchRequest{Items: makeItems(pc.items)}
		b.Run(pc.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := benchClient(ctx, cc, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkTransport_Bufconn(b *testing.B) {
	s := setupBenchServer()
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
	if _, err := benchClient(ctx, cc, &benchpb.BenchRequest{Items: makeItems(1)}); err != nil {
		b.Fatal(err)
	}

	for _, pc := range payloadConfigs {
		req := &benchpb.BenchRequest{Items: makeItems(pc.items)}
		b.Run(pc.name, func(b *testing.B) {
			b.ResetTimer()
			b.ReportAllocs()
			for b.Loop() {
				if _, err := benchClient(ctx, cc, req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
