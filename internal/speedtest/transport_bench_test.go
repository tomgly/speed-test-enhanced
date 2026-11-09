package speedtest

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Local-only benchmarks; Go does not run these without an explicit -bench flag.
// Both endpoints run in this process, so CPU and allocation profiles cover both.
func BenchmarkTransport(b *testing.B) {
	s, err := NewServer(ServerConfig{HTTPAddress: "[::1]:0", TCPAddress: "[::1]:0", UDPAddress: "[::1]:0"}, nil)
	if err != nil {
		b.Fatal(err)
	}
	if err = s.Start(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close(context.Background()) })
	for _, protocol := range []string{"http", "tcp"} {
		for _, direction := range []string{"upload", "download"} {
			for _, count := range []int{1, 4, 16} {
				b.Run(fmt.Sprintf("%s/%s/%d", protocol, direction, count), func(b *testing.B) {
					c := NewClient(s.Address())
					if err := c.Open(context.Background()); err != nil {
						b.Fatal(err)
					}
					defer c.Close()
					cfg := DefaultConfig()
					cfg.Protocol, cfg.Mode, cfg.Connections = protocol, "manual", count
					if _, err := c.Window(context.Background(), cfg, direction, count, 100*time.Millisecond, nil); err != nil {
						b.Fatal(err)
					}
					var total uint64
					var elapsed float64
					b.ReportAllocs()
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						r, err := c.Window(context.Background(), cfg, direction, count, time.Second, nil)
						if err != nil {
							b.Fatal(err)
						}
						total += r.Bytes
						elapsed += r.Seconds
					}
					b.StopTimer()
					b.SetBytes(int64(total / uint64(b.N)))
					b.ReportMetric(float64(total)*8/elapsed/1e9, "Gbps")
				})
			}
		}
	}
}
