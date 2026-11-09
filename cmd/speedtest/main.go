package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"time"

	web "speedtest-enhanced"
	"speedtest-enhanced/internal/speedtest"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(args []string) error {
	if len(args) == 0 {
		fmt.Println("Speed Test Enhanced\nUsage: speedtest server|client|benchmark|healthcheck [options]\n       speedtest --version")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	switch args[0] {
	case "version", "--version":
		return printVersion()
	case "server":
		return serve(ctx, args[1:])
	case "client":
		return client(ctx, args[1:])
	case "benchmark":
		return benchmark(ctx, args[1:])
	case "healthcheck":
		h := env("HTTP_ADDR", ":3000")
		if strings.HasPrefix(h, ":") {
			h = "127.0.0.1" + h
		}
		c := http.Client{Timeout: 2 * time.Second}
		r, e := c.Get("http://" + h + "/healthz")
		if e != nil {
			return e
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return errors.New("unhealthy server")
		}
		return nil
	case "help", "--help", "-h":
		return run(nil)
	default:
		return fmt.Errorf("unknown command: %s", args[0])
	}
}
func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func serve(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("server", flag.ContinueOnError)
	httpAddr := f.String("http", env("HTTP_ADDR", ":"+env("PORT", "3000")), "HTTP listen address")
	tcpAddr := f.String("tcp", env("TCP_ADDR", ":3001"), "native TCP/control listen address")
	udpAddr := f.String("udp", env("UDP_ADDR", ":3002"), "UDP listen address")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	s, err := speedtest.NewServer(speedtest.ServerConfig{HTTPAddress: *httpAddr, TCPAddress: *tcpAddr, UDPAddress: *udpAddr, IPv6Origin: os.Getenv("IPV6_ORIGIN"), IPv4Origin: os.Getenv("IPV4_ORIGIN"), CORSOrigin: os.Getenv("CORS_ORIGIN")}, web.Assets)
	if err != nil {
		return err
	}
	if err = s.Start(); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "Speed Test Enhanced: HTTP %s, TCP %s, UDP %s\n", s.HTTPListener.Addr(), s.TCPListener.Addr(), s.UDP.LocalAddr())
	<-ctx.Done()
	closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return s.Close(closeCtx)
}
func configFlags(f *flag.FlagSet, c *speedtest.Config) {
	f.StringVar(&c.Protocol, "protocol", c.Protocol, "tcp, udp or http")
	f.StringVar(&c.Mode, "mode", c.Mode, "auto or manual")
	f.IntVar(&c.Connections, "connections", c.Connections, "fixed count in manual, limit in auto (1..128)")
	f.Float64Var(&c.Duration, "duration", c.Duration, "seconds per direction (0.05..30), excluding warm-up")
	f.Float64Var(&c.Warmup, "warmup", c.Warmup, "separate warm-up seconds per direction (0..5)")
	f.Uint64Var(&c.Rate, "rate", c.Rate, "aggregate UDP payload bits/second")
	f.IntVar(&c.PacketSize, "packet-size", c.PacketSize, "UDP datagram bytes, including 32-byte header (64..65000)")
}
func client(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("client", flag.ContinueOnError)
	server := f.String("server", "127.0.0.1:3000", "HTTP control server host[:port] or URL")
	asJSON := f.Bool("json", false, "write unrounded JSON result to stdout")
	c := speedtest.DefaultConfig()
	configFlags(f, &c)
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	progress := func(p speedtest.Progress) {
		fmt.Fprintf(os.Stderr, "\r%-8s %7.2f Gbps  %.2f s  %d streams", p.Phase, p.BPS/1e9, p.Seconds, p.Connections)
	}
	if *asJSON {
		progress = nil
	}
	r, err := speedtest.NewClient(*server).Run(ctx, c, progress)
	if !*asJSON {
		fmt.Fprintln(os.Stderr)
	}
	if err != nil {
		return err
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(r)
	}
	fmt.Printf("Download %.3f Gbps (%d bytes / %.6f s, %d streams)\nUpload   %.3f Gbps (%d bytes / %.6f s, %d streams)\nPing %.3f ms, jitter %.3f ms\nClient %s  Server %s\n", r.Download.BPS/1e9, r.Download.Bytes, r.Download.Seconds, r.Download.Connections, r.Upload.BPS/1e9, r.Upload.Bytes, r.Upload.Seconds, r.Upload.Connections, r.Ping, r.Jitter, r.Client, r.Server)
	if c.Protocol == "udp" {
		for _, v := range []struct {
			name string
			r    speedtest.Transfer
		}{{"Download", r.Download}, {"Upload", r.Upload}} {
			fmt.Printf("%s UDP: sent %d, received %d, lost %d, duplicates %d, reordered %d, packet jitter %.3f ms\n", v.name, v.r.SentPackets, v.r.ReceivedPackets, v.r.LostPackets, v.r.DuplicatePackets, v.r.ReorderedPackets, v.r.Jitter)
		}
	}
	return nil
}

type benchmarkRow struct {
	Repeat          int              `json:"repeat"`
	Result          speedtest.Result `json:"result"`
	WallSeconds     float64          `json:"wallSeconds"`
	CPUSeconds      float64          `json:"cpuSeconds"`
	CPUCores        float64          `json:"cpuCores"`
	HeapPeakBytes   uint64           `json:"heapPeakBytes"`
	GoReservedBytes uint64           `json:"goReservedBytes"`
	AllocatedBytes  uint64           `json:"allocatedBytes"`
	GCCount         uint32           `json:"gcCount"`
	Error           string           `json:"error,omitempty"`
}

func benchmark(ctx context.Context, args []string) error {
	f := flag.NewFlagSet("benchmark", flag.ContinueOnError)
	server := f.String("server", "", "external server URL; empty runs an in-process loopback server")
	counts := f.String("connections", "1,4,16,32", "comma-separated manual connection counts")
	protocols := f.String("protocols", "tcp,udp,http", "comma-separated protocols")
	duration := f.Float64("duration", 1, "measured seconds per direction")
	warmup := f.Float64("warmup", .2, "separate warm-up seconds")
	rate := f.Uint64("rate", 1_000_000_000, "aggregate UDP payload bps")
	packetSize := f.Int("packet-size", 1400, "UDP datagram bytes including test header")
	repeats := f.Int("repeats", 1, "repetitions of every condition")
	profile := f.String("cpu-profile", "", "optional local CPU profile filename")
	if err := f.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *repeats < 1 || *repeats > 100 {
		return errors.New("repeats must be 1..100")
	}
	scope := "client process only"
	if *server == "" {
		s, err := speedtest.NewServer(speedtest.ServerConfig{HTTPAddress: "127.0.0.1:0", TCPAddress: "127.0.0.1:0", UDPAddress: "127.0.0.1:0"}, web.Assets)
		if err != nil {
			return err
		}
		if err = s.Start(); err != nil {
			return err
		}
		defer s.Close(context.Background())
		*server = s.Address()
		scope = "combined server and client, in-process loopback"
	}
	if *profile != "" {
		f, err := os.Create(*profile)
		if err != nil {
			return err
		}
		defer f.Close()
		if err = pprof.StartCPUProfile(f); err != nil {
			return err
		}
		defer pprof.StopCPUProfile()
	}
	report := struct {
		Started     time.Time      `json:"started"`
		OS          string         `json:"os"`
		Arch        string         `json:"arch"`
		Go          string         `json:"go"`
		LogicalCPUs int            `json:"logicalCPUs"`
		GOMAXPROCS  int            `json:"gomaxprocs"`
		Scope       string         `json:"resourceScope"`
		Server      string         `json:"server"`
		Rows        []benchmarkRow `json:"rows"`
	}{Started: time.Now().UTC(), OS: runtime.GOOS, Arch: runtime.GOARCH, Go: runtime.Version(), LogicalCPUs: runtime.NumCPU(), GOMAXPROCS: runtime.GOMAXPROCS(0), Scope: scope, Server: *server}
	failed := false
	for _, protocol := range strings.Split(*protocols, ",") {
		for _, count := range strings.Split(*counts, ",") {
			n, err := strconv.Atoi(count)
			if err != nil {
				return err
			}
			c := speedtest.DefaultConfig()
			c.Protocol = protocol
			c.Mode = "manual"
			c.Connections = n
			c.Duration = *duration
			c.Warmup = *warmup
			c.Rate = *rate
			c.PacketSize = *packetSize
			if err = c.Validate(); err != nil {
				return err
			}
			for i := 1; i <= *repeats; i++ {
				runtime.GC()
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				peak := before.HeapAlloc
				start := time.Now()
				cpu := processCPUSeconds()
				r, err := speedtest.NewClient(*server).Run(ctx, c, func(speedtest.Progress) {
					var m runtime.MemStats
					runtime.ReadMemStats(&m)
					peak = max(peak, m.HeapAlloc)
				})
				wall := time.Since(start).Seconds()
				cpu = processCPUSeconds() - cpu
				runtime.ReadMemStats(&after)
				row := benchmarkRow{Repeat: i, Result: r, WallSeconds: wall, CPUSeconds: cpu, CPUCores: cpu / wall, HeapPeakBytes: max(peak, after.HeapAlloc), GoReservedBytes: after.Sys, AllocatedBytes: after.TotalAlloc - before.TotalAlloc, GCCount: after.NumGC - before.NumGC}
				if wall <= 0 {
					row.CPUCores = 0
				}
				if err != nil {
					row.Result.Config = c
					row.Error = err.Error()
					failed = true
				}
				report.Rows = append(report.Rows, row)
				fmt.Fprintf(os.Stderr, "%s/%d #%d: download %.3f upload %.3f Gbps %s\n", protocol, n, i, r.Download.BPS/1e9, r.Upload.BPS/1e9, row.Error)
				if ctx.Err() != nil {
					break
				}
			}
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return err
	}
	if failed {
		return errors.New("one or more benchmark conditions failed; see JSON errors")
	}
	return nil
}
