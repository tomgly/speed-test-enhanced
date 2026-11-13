package speedtest

import (
	"sync"
	"syscall"
	"unsafe"
)

var (
	kernel32                = syscall.NewLazyDLL("kernel32.dll")
	queryPerformanceCounter = kernel32.NewProc("QueryPerformanceCounter")
	performanceFrequency    = sync.OnceValue(func() int64 {
		var frequency int64
		kernel32.NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&frequency)))
		return frequency
	})
)

func pingNow() int64 {
	// The Windows monotonic clock in Go 1.27 and earlier can miss short RTTs.
	var ticks int64
	queryPerformanceCounter.Call(uintptr(unsafe.Pointer(&ticks)))
	return ticks
}

func pingMillisecondsSince(start int64) float64 {
	return float64(pingNow()-start) * 1000 / float64(performanceFrequency())
}
