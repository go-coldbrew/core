package benchmarks

import (
	"fmt"
	"testing"

	"github.com/go-coldbrew/core/benchmarks/benchpb"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// makeItems builds a realistic payload with n items containing
// multiple field types: strings, numbers, maps, repeated fields, timestamps.
func makeItems(n int) []*benchpb.Item {
	items := make([]*benchpb.Item, n)
	for i := range items {
		items[i] = &benchpb.Item{
			Id:          fmt.Sprintf("item-%06d", i),
			Name:        fmt.Sprintf("Product %d - Premium Widget", i),
			Description: "A high-quality widget designed for enterprise use. Features include durability, scalability, and compatibility with existing systems.",
			Price:       99.99 + float64(i)*0.01,
			Quantity:    int64(100 + i),
			Metadata: map[string]string{
				"sku":      fmt.Sprintf("SKU-%06d", i),
				"category": "electronics",
				"vendor":   "acme-corp",
				"region":   "us-west-2",
			},
			Tags:      []string{"premium", "electronics", "enterprise", "widget"},
			CreatedAt: timestamppb.Now(),
		}
	}
	return items
}

var payloadConfigs = []struct {
	name  string
	items int
}{
	{"1item", 1},
	{"50items", 50},
	{"500items", 500},
}

// --- Proto codec: standard proto.Marshal / proto.Unmarshal ---

func BenchmarkCodec_Proto_Marshal(b *testing.B) {
	for _, pc := range payloadConfigs {
		req := &benchpb.BenchRequest{Items: makeItems(pc.items)}
		b.Run(pc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := proto.Marshal(req); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCodec_Proto_Unmarshal(b *testing.B) {
	for _, pc := range payloadConfigs {
		req := &benchpb.BenchRequest{Items: makeItems(pc.items)}
		data, err := proto.Marshal(req)
		if err != nil {
			b.Fatal(err)
		}
		b.Run(pc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				out := &benchpb.BenchRequest{}
				if err := proto.Unmarshal(data, out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// --- VTProto codec: generated MarshalVT / UnmarshalVT ---

func BenchmarkCodec_VTProto_Marshal(b *testing.B) {
	for _, pc := range payloadConfigs {
		req := &benchpb.BenchRequest{Items: makeItems(pc.items)}
		b.Run(pc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := req.MarshalVT(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkCodec_VTProto_Unmarshal(b *testing.B) {
	for _, pc := range payloadConfigs {
		req := &benchpb.BenchRequest{Items: makeItems(pc.items)}
		data, err := req.MarshalVT()
		if err != nil {
			b.Fatal(err)
		}
		b.Run(pc.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				out := &benchpb.BenchRequest{}
				if err := out.UnmarshalVT(data); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
