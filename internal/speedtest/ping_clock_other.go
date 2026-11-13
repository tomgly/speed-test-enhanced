//go:build !windows

package speedtest

import "time"

func pingNow() time.Time {
	return time.Now()
}

func pingMillisecondsSince(start time.Time) float64 {
	return float64(time.Since(start)) / float64(time.Millisecond)
}
