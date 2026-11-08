package speedtest

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"
)

func TestConfig(t *testing.T) {
	c := DefaultConfig()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Config){func(c *Config) { c.Connections = 0 }, func(c *Config) { c.Connections = 129 }, func(c *Config) { c.Duration = math.NaN() }, func(c *Config) { c.Duration = math.Inf(1) }, func(c *Config) { c.Warmup = -1 }, func(c *Config) { c.Mode = "bad" }, func(c *Config) { c.Protocol = "bad" }, func(c *Config) { c.PacketSize = 32 }, func(c *Config) { c.Rate = 0 }} {
		c := DefaultConfig()
		change(&c)
		if c.Validate() == nil {
			t.Fatalf("accepted invalid config: %+v", c)
		}
	}
}
func TestLatency(t *testing.T) {
	r := SummarizeLatency([]float64{1.25, 3.25, 2.25, 4.25})
	if r.Ping != 2.75 || math.Abs(r.Jitter-5.0/3) > 1e-12 {
		t.Fatal(r)
	}
	if SummarizeLatency(nil) != (Latency{}) {
		t.Fatal("empty latency")
	}
	if SummarizeLatency([]float64{2}).Ping != 2 {
		t.Fatal("singleton")
	}
}
func TestSelectConnections(t *testing.T) {
	for _, tc := range []struct {
		name   string
		limit  int
		rates  []float64
		want   int
		counts []int
	}{
		{"growth", 128, []float64{1, 2, 4, 8, 16, 32, 64, 128}, 128, []int{1, 2, 4, 8, 16, 32, 64, 128}},
		{"plateau", 128, []float64{10, 10.5}, 1, []int{1, 2}},
		{"regression", 128, []float64{10, 5}, 1, []int{1, 2}},
		{"bounded", 17, []float64{1, 2, 4, 8, 16, 17}, 16, []int{1, 2, 4, 8, 16, 17}},
		{"intermediate plateau", 16, []float64{1, 2, 2.05}, 2, []int{1, 2, 4}},
		{"one", 1, nil, 1, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var counts []int
			calls := 0
			got, err := SelectConnections(context.Background(), tc.limit, func(_ context.Context, n int) (float64, error) {
				if calls%ProbeSamples == 0 {
					counts = append(counts, n)
				}
				calls++
				return tc.rates[(calls-1)/ProbeSamples], nil
			})
			if err != nil || got != tc.want || !reflect.DeepEqual(counts, tc.counts) {
				t.Fatalf("%d %v %v", got, counts, err)
			}
		})
	}
	sentinel := errors.New("probe failed")
	if _, err := SelectConnections(context.Background(), 4, func(context.Context, int) (float64, error) { return 0, sentinel }); !errors.Is(err, sentinel) {
		t.Fatal(err)
	}
}

func TestCalibrationRejectsTransientOutliers(t *testing.T) {
	for _, rates := range [][]float64{
		{1000, 10, 10, 20, 1, 20, 21, 21, 1000},
		{1, 10, 10, 20, 1000, 20, 21, 21, 1},
	} {
		calls := 0
		got, err := SelectConnections(context.Background(), 16, func(_ context.Context, n int) (float64, error) {
			v := rates[calls]
			calls++
			return v, nil
		})
		if err != nil || got != 2 || calls != 9 {
			t.Fatal(got, calls, err)
		}
	}
	for _, bad := range []float64{math.NaN(), math.Inf(1), -1} {
		if _, err := SelectConnections(context.Background(), 4, func(context.Context, int) (float64, error) { return bad, nil }); err == nil {
			t.Fatal("invalid calibration accepted", bad)
		}
	}
}
func TestRunDirectionSeparatesWindowsAndFixesConcurrency(t *testing.T) {
	for _, mode := range []string{"manual", "auto"} {
		for _, direction := range []string{"download", "upload"} {
			t.Run(mode+direction, func(t *testing.T) {
				c := DefaultConfig()
				c.Mode = mode
				c.Connections = 4
				c.Duration = .1
				c.Warmup = .05
				measured := false
				var used []int
				var durations []time.Duration
				r, err := RunDirection(context.Background(), c, direction, func(_ context.Context, dir string, n int, d time.Duration, live func(Transfer)) (Transfer, error) {
					if dir != direction {
						t.Fatal(dir)
					}
					used = append(used, n)
					durations = append(durations, d)
					if live != nil {
						measured = true
						live(transfer(100, d, n))
						return transfer(100, d, n), nil
					}
					return transfer(uint64(n)*9_000_000, d, n), nil
				}, func(p Progress) {
					if p.Phase != direction || p.Connections != 4 {
						t.Fatal(p)
					}
				})
				if err != nil || !measured || r.Bytes != 100 || r.Seconds != .1 || r.BPS != 8000 {
					t.Fatalf("%+v %v", r, err)
				}
				if used[len(used)-1] != 4 || used[len(used)-2] != 4 || durations[len(durations)-2] != 50*time.Millisecond {
					t.Fatal(used, durations)
				}
			})
		}
	}
}
func TestDirectionFailureDoesNotReturnPartialResult(t *testing.T) {
	c := DefaultConfig()
	c.Mode = "manual"
	c.Warmup = 0
	r, err := RunDirection(context.Background(), c, "download", func(context.Context, string, int, time.Duration, func(Transfer)) (Transfer, error) {
		return Transfer{Bytes: 123}, context.Canceled
	}, nil)
	if !errors.Is(err, context.Canceled) || r != (Transfer{}) {
		t.Fatal(r, err)
	}
}
func TestPacketAccounting(t *testing.T) {
	var r packetTracker
	r.observe(1, 0, 100, 1_000_000)
	r.observe(3, 2_000_000, 100, 4_000_000)
	r.observe(2, 1_000_000, 100, 5_000_000)
	r.observe(3, 2_000_000, 100, 6_000_000)
	if r.Bytes != 300 || r.Packets != 3 || r.Duplicates != 1 || r.Reordered != 1 || r.Jitter != 1 {
		t.Fatalf("%+v", r.packetStats)
	}
	r.observe(70000, 7_000_000, 100, 8_000_000)
	r.observe(1, 0, 100, 9_000_000)
	if r.Packets != 4 {
		t.Fatal("stale sequence counted")
	}
}
func BenchmarkPacketAccounting(b *testing.B) {
	var r packetTracker
	b.ReportAllocs()
	b.SetBytes(1368)
	for i := 0; i < b.N; i++ {
		r.observe(uint64(i+1), int64(i)*1000, 1368, int64(i)*1000+1)
	}
}

func TestPacketWindowWrap(t *testing.T) {
	var r packetTracker
	for seq := uint64(1); seq <= 65537; seq++ {
		r.observe(seq, int64(seq), 100, int64(seq)+1)
	}
	r.observe(65536, 65536, 100, 65539)
	r.observe(1, 1, 100, 65540)
	if r.Packets != 65537 || r.Duplicates != 1 {
		t.Fatal(r.packetStats)
	}
}
func TestUDPPacingHonorsShortDeadlineAtLowRate(t *testing.T) {
	start := time.Now()
	count, err := sendUDP(context.Background(), .05, 1, 1400, [16]byte{}, func(b []byte) (int, error) { return len(b), nil })
	if err != nil || count > 1 || time.Since(start) > time.Second {
		t.Fatal(count, err, time.Since(start))
	}
}

func TestResultValidation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = "manual"
	cfg.Connections = 1
	cfg.Duration = 1
	r := Result{Config: cfg, Download: transfer(125_000_000, time.Second, 1), Upload: transfer(125_000_000, time.Second, 1)}
	counts := &byteCounters{}
	counts.download.Store(r.Download.Bytes)
	counts.upload.Store(r.Upload.Bytes)
	if err := ValidateResult(r, counts); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Result){
		func(r *Result) { r.Download.BPS += 100 },
		func(r *Result) { r.Download.BPS = math.NaN() },
		func(r *Result) { r.Upload.Bytes++ },
		func(r *Result) { r.Download.Seconds = .5 },
		func(r *Result) { r.Download.Connections = 2 },
		func(r *Result) { r.Ping = -1 },
		func(r *Result) { r.Jitter = math.Inf(1) },
	} {
		invalid := r
		change(&invalid)
		if ValidateResult(invalid, counts) == nil {
			t.Fatal("invalid result accepted", invalid)
		}
	}
	r.Config.Protocol = "udp"
	r.Config.PacketSize = 1400
	if ValidateResult(r, counts) == nil {
		t.Fatal("missing UDP receipts accepted")
	}
}
