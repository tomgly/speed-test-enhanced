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

// UDP profiling is local-only and includes both endpoints and final control data.
func BenchmarkUDPTransport(b *testing.B) {
	s, err := NewServer(ServerConfig{HTTPAddress: "[::1]:0", TCPAddress: "[::1]:0", UDPAddress: "[::1]:0"}, nil)
	if err != nil {
		b.Fatal(err)
	}
	if err = s.Start(); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { s.Close(context.Background()) })
	for _, direction := range []string{"download", "upload"} {
		for _, count := range []int{1, 4, 16} {
			b.Run(fmt.Sprintf("%s/%d", direction, count), func(b *testing.B) {
				c := NewClient(s.Address())
				if err := c.Open(context.Background()); err != nil {
					b.Fatal(err)
				}
				defer c.Close()
				cfg := DefaultConfig()
				cfg.Protocol, cfg.Mode, cfg.Connections, cfg.Rate = "udp", "manual", count, 10_000_000_000
				var total, sent, received uint64
				var elapsed float64
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					r, err := c.Window(context.Background(), cfg, direction, count, time.Second, nil)
					if err != nil {
						b.Fatal(err)
					}
					total += r.Bytes
					sent += r.SentPackets
					received += r.ReceivedPackets
					elapsed += r.Seconds
				}
				b.StopTimer()
				b.SetBytes(int64(total / uint64(b.N)))
				b.ReportMetric(float64(total)*8/elapsed/1e9, "received-Gbps")
				b.ReportMetric(float64(sent)*float64(cfg.PacketSize-udpHeader)*8/elapsed/1e9, "sent-Gbps")
				if sent > 0 {
					b.ReportMetric(float64(sent-received)*100/float64(sent), "loss-percent")
				}
			})
		}
	}
}

func BenchmarkUDPPacing(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := sendUDP(context.Background(), .05, 1_000_000, 1400, [16]byte{}, func(p []byte) (int, error) { return len(p), nil }); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkUDPReceiveBuffer(b *testing.B) {
	for _, mib := range []int{4, 16} {
		b.Run(fmt.Sprintf("%dMiB", mib), func(b *testing.B) {
			s, err := NewServer(ServerConfig{HTTPAddress: "[::1]:0", TCPAddress: "[::1]:0", UDPAddress: "[::1]:0"}, nil)
			if err != nil {
				b.Fatal(err)
			}
			if err = s.Start(); err != nil {
				b.Fatal(err)
			}
			defer s.Close(context.Background())
			if err := s.UDP.SetReadBuffer(mib * 1024 * 1024); err != nil {
				b.Fatal(err)
			}
			c := NewClient(s.Address())
			if err := c.Open(context.Background()); err != nil {
				b.Fatal(err)
			}
			defer c.Close()
			cfg := DefaultConfig()
			cfg.Protocol, cfg.Mode, cfg.Connections, cfg.Rate = "udp", "manual", 16, 25_000_000_000
			var bytes, sent, received uint64
			var elapsed float64
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				r, err := c.Window(context.Background(), cfg, "upload", 16, 2*time.Second, nil)
				if err != nil {
					b.Fatal(err)
				}
				bytes += r.Bytes
				sent += r.SentPackets
				received += r.ReceivedPackets
				elapsed += r.Seconds
			}
			b.StopTimer()
			b.ReportMetric(float64(bytes)*8/elapsed/1e9, "received-Gbps")
			b.ReportMetric(float64(sent-received)*100/float64(sent), "loss-percent")
		})
	}
}
