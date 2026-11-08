package speedtest

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
)

type byteCountersKey struct{}
type byteCounters struct{ download, upload atomic.Uint64 }

func recordBytes(ctx context.Context, direction string, n uint64) {
	counts, _ := ctx.Value(byteCountersKey{}).(*byteCounters)
	if counts == nil {
		return
	}
	if direction == "download" {
		counts.download.Add(n)
	} else {
		counts.upload.Add(n)
	}
}

// ValidateResult checks arithmetic and confirmed receiver totals against the
// server's observed payload upper bound. This does not certify the client clock.
func ValidateResult(r Result, counts *byteCounters) error {
	if err := r.Config.Validate(); err != nil {
		return err
	}
	for _, v := range []float64{r.Ping, r.Jitter} {
		if v < 0 || math.IsNaN(v) || math.IsInf(v, 0) {
			return errors.New("invalid latency")
		}
	}
	for i, v := range []Transfer{r.Download, r.Upload} {
		if v.Bytes == 0 || v.Seconds < r.Config.Duration || v.Seconds > r.Config.Duration+8 || math.IsNaN(v.Seconds) || math.IsInf(v.Seconds, 0) || v.Connections < 1 || v.Connections > r.Config.Connections || (r.Config.Mode == "manual" && v.Connections != r.Config.Connections) {
			return errors.New("invalid transfer interval or concurrency")
		}
		expected := float64(v.Bytes) * 8 / v.Seconds
		if math.IsNaN(v.BPS) || math.IsInf(v.BPS, 0) || math.Abs(v.BPS-expected) > max(1, expected*1e-9) {
			return errors.New("inconsistent transfer rate")
		}
		if r.Config.Protocol == "udp" && (v.ReceivedPackets > v.SentPackets || v.LostPackets != v.SentPackets-v.ReceivedPackets || v.Bytes != v.ReceivedPackets*uint64(r.Config.PacketSize-udpHeader) || v.Jitter < 0 || math.IsNaN(v.Jitter) || math.IsInf(v.Jitter, 0)) {
			return errors.New("inconsistent UDP statistics")
		}
		if counts != nil {
			observed := counts.download.Load()
			if i == 1 {
				observed = counts.upload.Load()
			}
			if v.Bytes > observed {
				return errors.New("result exceeds server-observed data")
			}
		}
	}
	return nil
}
