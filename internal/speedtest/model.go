package speedtest

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"
)

const BufferSize = 256 * 1024

const ProbeDuration = 400 * time.Millisecond
const GainThreshold = 1.08
const ProbeFactor = 2
const ProbeSamples = 3

type Config struct {
	Protocol    string  `json:"protocol"`
	Mode        string  `json:"mode"`
	Connections int     `json:"connections"`
	Duration    float64 `json:"duration"`
	Warmup      float64 `json:"warmup"`
	Rate        uint64  `json:"rate"` // UDP aggregate payload bits per second.
	PacketSize  int     `json:"packetSize"`
}

func DefaultConfig() Config {
	return Config{"tcp", "auto", 32, 10, 1, 1_000_000_000, 1400}
}
func (c Config) Validate() error {
	if c.Protocol != "tcp" && c.Protocol != "udp" && c.Protocol != "http" {
		return errors.New("protocol must be tcp, udp or http")
	}
	if c.Mode != "auto" && c.Mode != "manual" {
		return errors.New("mode must be auto or manual")
	}
	if c.Connections < 1 || c.Connections > 128 {
		return errors.New("connections must be 1..128")
	}
	if math.IsNaN(c.Duration) || math.IsInf(c.Duration, 0) || c.Duration < .05 || c.Duration > 30 {
		return errors.New("duration must be 0.05..30 seconds")
	}
	if math.IsNaN(c.Warmup) || math.IsInf(c.Warmup, 0) || c.Warmup < 0 || c.Warmup > 5 {
		return errors.New("warmup must be 0..5 seconds")
	}
	if c.PacketSize < 64 || c.PacketSize > 65000 || c.Rate < 1 || c.Rate > 1_000_000_000_000 {
		return errors.New("packet size must be 64..65000 and UDP rate 1..1e12 bps")
	}
	return nil
}
func seconds(n float64) time.Duration { return time.Duration(n * float64(time.Second)) }

type Transfer struct {
	Bytes            uint64  `json:"bytes"`
	Seconds          float64 `json:"seconds"`
	BPS              float64 `json:"bps"`
	Connections      int     `json:"connections"`
	SentPackets      uint64  `json:"sentPackets,omitempty"`
	ReceivedPackets  uint64  `json:"receivedPackets,omitempty"`
	LostPackets      uint64  `json:"lostPackets,omitempty"`
	DuplicatePackets uint64  `json:"duplicatePackets,omitempty"`
	ReorderedPackets uint64  `json:"reorderedPackets,omitempty"`
	Jitter           float64 `json:"jitter,omitempty"`
}

func transfer(bytes uint64, elapsed time.Duration, count int) Transfer {
	s := elapsed.Seconds()
	r := Transfer{Bytes: bytes, Seconds: s, Connections: count}
	if s > 0 {
		r.BPS = float64(bytes) * 8 / s
	}
	return r
}

type Latency struct {
	Ping   float64 `json:"ping"`
	Jitter float64 `json:"jitter"`
}

func SummarizeLatency(samples []float64) Latency {
	if len(samples) == 0 {
		return Latency{}
	}
	ordered := append([]float64(nil), samples...)
	sort.Float64s(ordered)
	middle := len(ordered) / 2
	ping := ordered[middle]
	if len(ordered)%2 == 0 {
		ping = (ping + ordered[middle-1]) / 2
	}
	jitter := 0.0
	for i := 1; i < len(samples); i++ {
		jitter += math.Abs(samples[i] - samples[i-1])
	}
	if len(samples) > 1 {
		jitter /= float64(len(samples) - 1)
	}
	return Latency{ping, jitter}
}

type Result struct {
	Config Config `json:"config"`
	Latency
	Download Transfer  `json:"download"`
	Upload   Transfer  `json:"upload"`
	Client   string    `json:"client"`
	Server   string    `json:"server"`
	Started  time.Time `json:"started"`
}
type Progress struct {
	Phase string `json:"phase"`
	Transfer
}
type WindowFunc func(context.Context, string, int, time.Duration, func(Transfer)) (Transfer, error)

// Compare medians of independent windows so a single transient spike or dip
// cannot stop calibration or select excessive parallelism. Keep the smaller
// count unless doubling provides an 8% gain (at most 24 probe windows).
func SelectConnections(ctx context.Context, limit int, probe func(context.Context, int) (float64, error)) (int, error) {
	if limit == 1 {
		return 1, nil
	}
	best, bestRate := 1, 0.0
	for n := 1; ; n = min(limit, n*ProbeFactor) {
		var samples [ProbeSamples]float64
		for i := range samples {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			bps, err := probe(ctx, n)
			if err != nil {
				return 0, err
			}
			if math.IsNaN(bps) || math.IsInf(bps, 0) || bps < 0 {
				return 0, errors.New("invalid calibration result")
			}
			samples[i] = bps
		}
		sort.Float64s(samples[:])
		bps := samples[ProbeSamples/2]
		if bps > bestRate*GainThreshold {
			best, bestRate = n, bps
		} else if n > 1 {
			break
		}
		if n == limit {
			break
		}
	}
	return best, nil
}

func RunDirection(ctx context.Context, c Config, direction string, window WindowFunc, progress func(Progress)) (Transfer, error) {
	n := c.Connections
	if c.Mode == "auto" {
		var err error
		n, err = SelectConnections(ctx, n, func(ctx context.Context, n int) (float64, error) {
			r, e := window(ctx, direction, n, ProbeDuration, nil)
			return r.BPS, e
		})
		if err != nil {
			return Transfer{}, err
		}
	}
	if c.Warmup > 0 {
		if _, err := window(ctx, direction, n, seconds(c.Warmup), nil); err != nil {
			return Transfer{}, err
		}
	}
	r, err := window(ctx, direction, n, seconds(c.Duration), func(t Transfer) {
		if progress != nil {
			progress(Progress{direction, t})
		}
	})
	if err != nil {
		return Transfer{}, err
	}
	if r.Bytes == 0 || r.Seconds <= 0 || math.IsNaN(r.BPS) || math.IsInf(r.BPS, 0) {
		return Transfer{}, errors.New("no confirmed measurement data")
	}
	return r, nil
}
