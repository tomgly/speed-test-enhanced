package main

import "syscall"

func processCPUSeconds() float64 {
	h, err := syscall.GetCurrentProcess()
	if err != nil {
		return 0
	}
	var create, exit, kernel, user syscall.Filetime
	if syscall.GetProcessTimes(h, &create, &exit, &kernel, &user) != nil {
		return 0
	}
	ticks := func(v syscall.Filetime) uint64 { return uint64(v.HighDateTime)<<32 | uint64(v.LowDateTime) }
	return float64(ticks(kernel)+ticks(user)) / 1e7
}
