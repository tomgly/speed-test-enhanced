package speedtest

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Client struct {
	BaseURL string
	HTTP    *http.Client
	session SessionInfo
	payload []byte
}

func NewClient(server string) *Client {
	if !strings.Contains(server, "://") {
		if net.ParseIP(strings.Trim(server, "[]")) != nil {
			server = net.JoinHostPort(strings.Trim(server, "[]"), "3000")
		} else if _, _, err := net.SplitHostPort(server); err != nil {
			server = net.JoinHostPort(server, "3000")
		}
		server = "http://" + server
	}
	transport := &http.Transport{MaxIdleConns: 256, MaxIdleConnsPerHost: 128, MaxConnsPerHost: 128, DisableCompression: true, ResponseHeaderTimeout: 5 * time.Second, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext}
	return &Client{BaseURL: strings.TrimRight(server, "/"), HTTP: &http.Client{Transport: transport, Timeout: 40 * time.Second}}
}
func (c *Client) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode != 200 {
		res.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", path, res.StatusCode)
	}
	return res, nil
}
func (c *Client) Open(ctx context.Context) error {
	r, err := c.request(ctx, "POST", "/api/sessions", nil)
	if err != nil {
		return err
	}
	defer r.Body.Close()
	return json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&c.session)
}
func (c *Client) Close() {
	if c.session.ID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, "DELETE", c.BaseURL+"/api/session?id="+url.QueryEscape(c.session.ID), nil)
		if req != nil {
			if r, e := c.HTTP.Do(req); e == nil {
				r.Body.Close()
			}
		}
	}
	c.HTTP.CloseIdleConnections()
}
func (c *Client) Ping(ctx context.Context) (Latency, error) {
	samples := make([]float64, 0, 10)
	for i := 0; i < 11; i++ {
		start := pingNow()
		r, err := c.request(ctx, "HEAD", "/api/ping", nil)
		if err != nil {
			return Latency{}, err
		}
		r.Body.Close()
		if i > 0 {
			samples = append(samples, pingMillisecondsSince(start))
		}
	}
	return SummarizeLatency(samples), nil
}
func (c *Client) Run(ctx context.Context, config Config, progress func(Progress)) (Result, error) {
	if err := config.Validate(); err != nil {
		return Result{}, err
	}
	if err := c.Open(ctx); err != nil {
		return Result{}, err
	}
	defer c.Close()
	r := Result{Config: config, Started: time.Now().UTC(), Client: c.session.Client, Server: c.session.Server}
	var err error
	r.Latency, err = c.Ping(ctx)
	if err != nil {
		return Result{}, err
	}
	window := func(ctx context.Context, direction string, n int, d time.Duration, live func(Transfer)) (Transfer, error) {
		return c.Window(ctx, config, direction, n, d, live)
	}
	r.Download, err = RunDirection(ctx, config, "download", window, progress)
	if err != nil {
		return Result{}, fmt.Errorf("download: %w", err)
	}
	r.Upload, err = RunDirection(ctx, config, "upload", window, progress)
	if err != nil {
		return Result{}, fmt.Errorf("upload: %w", err)
	}
	if err = ValidateResult(r, nil); err != nil {
		return Result{}, err
	}
	body, err := json.Marshal(r)
	if err != nil {
		return Result{}, err
	}
	response, err := c.request(ctx, "POST", "/api/results?id="+url.QueryEscape(c.session.ID), bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	response.Body.Close()
	return r, nil
}

func (c *Client) Window(ctx context.Context, config Config, direction string, n int, d time.Duration, live func(Transfer)) (Transfer, error) {
	if err := config.Validate(); err != nil {
		return Transfer{}, err
	}
	if n < 1 || n > 128 || d < 50*time.Millisecond || d > 30*time.Second {
		return Transfer{}, errors.New("invalid window limits")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if direction != "download" && direction != "upload" {
		return Transfer{}, errors.New("invalid direction")
	}
	h := handshake{c.session.ID, config.Protocol, direction, d.Seconds(), max(1, config.Rate/uint64(n)), config.PacketSize}
	streams := make([]*nativeStream, n)
	if config.Protocol != "http" {
		u, err := url.Parse(c.BaseURL)
		if err != nil {
			return Transfer{}, err
		}
		address := net.JoinHostPort(u.Hostname(), strconv.Itoa(c.session.TCPPort))
		// Preparation finishes before the shared measurement clock starts.
		errs := make(chan error, n)
		var wg sync.WaitGroup
		for i := range streams {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				v, e := prepareStream(ctx, address, c.session.UDPPort, h)
				streams[i] = v
				errs <- e
			}(i)
		}
		wg.Wait()
		close(errs)
		var failure error
		for err := range errs {
			if err != nil {
				failure = err
			}
		}
		defer func() {
			for _, v := range streams {
				if v != nil {
					v.close()
				}
			}
		}()
		if failure != nil {
			return Transfer{}, failure
		}
	}
	if direction == "upload" && config.Protocol != "udp" {
		size := BufferSize
		if config.Protocol == "http" {
			size = 8 * 1024 * 1024
		}
		if len(c.payload) < size {
			c.payload = make([]byte, size)
			if _, err := rand.Read(c.payload); err != nil {
				return Transfer{}, err
			}
		}
	}
	var confirmed atomic.Uint64
	var start time.Time
	ready := sync.WaitGroup{}
	ready.Add(n)
	begin := make(chan struct{})
	done := make(chan struct{})
	finished := make(chan time.Time, n)
	stats := make(chan packetStats, n)
	errs := make(chan error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			var buffer []byte
			if direction == "download" && config.Protocol != "udp" {
				buffer = make([]byte, BufferSize)
			} else if config.Protocol == "tcp" {
				// Connections only read the common payload; no per-stream copy.
				buffer = c.payload[:BufferSize]
			}
			ready.Done()
			<-begin
			var st packetStats
			var err error
			if config.Protocol == "http" {
				err = c.httpStream(ctx, h, &confirmed, start, buffer)
			} else {
				st, err = streams[i].run(ctx, h, &confirmed, buffer)
			}
			finished <- time.Now()
			if err != nil {
				errs <- err
				cancel()
			}
			stats <- st
		}(i)
	}
	ready.Wait()
	start = time.Now()
	close(begin)
	go func() { wg.Wait(); close(done) }()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	var lastBytes uint64
	last := start
	for {
		select {
		case <-ticker.C:
			if live != nil {
				now := time.Now()
				b := confirmed.Load()
				r := transfer(b-lastBytes, now.Sub(last), n)
				r.Bytes = b
				r.Seconds = now.Sub(start).Seconds()
				live(r)
				lastBytes = b
				last = now
			}
		case <-done:
			// Progress callbacks may block; only transfer completion belongs in
			// the denominator, including the final receipt and drain.
			end := start
			for i := 0; i < n; i++ {
				if t := <-finished; t.After(end) {
					end = t
				}
			}
			elapsed := end.Sub(start)
			close(errs)
			for err := range errs {
				if err != nil {
					return Transfer{}, err
				}
			}
			if err := ctx.Err(); err != nil {
				return Transfer{}, err
			}
			if elapsed < d {
				return Transfer{}, errors.New("transfer ended before the measurement deadline")
			}
			r := transfer(confirmed.Load(), elapsed, n)
			close(stats)
			jitterWeight := uint64(0)
			for st := range stats {
				r.SentPackets += st.Sent
				r.ReceivedPackets += st.Packets
				r.DuplicatePackets += st.Duplicates
				r.ReorderedPackets += st.Reordered
				r.Jitter += st.Jitter * float64(st.JitterSamples)
				jitterWeight += st.JitterSamples
			}
			if r.SentPackets < r.ReceivedPackets {
				return Transfer{}, errors.New("invalid UDP packet counts")
			}
			r.LostPackets = r.SentPackets - r.ReceivedPackets
			if jitterWeight > 0 {
				r.Jitter /= float64(jitterWeight)
			}
			return r, nil
		}
	}
}

// Preserve WriterTo with chunked framing: a known length makes net/http wrap
// the body in a LimitedReader and copy through a 32 KiB buffer.
type uploadBody struct{ *bytes.Reader }

func (b uploadBody) WriteTo(w io.Writer) (int64, error) {
	return b.Reader.WriteTo(uploadWriter{w})
}

type uploadWriter struct{ io.Writer }

func (w uploadWriter) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		chunk := b[:min(len(b), BufferSize)]
		n, err := w.Writer.Write(chunk)
		total += n
		if err != nil {
			return total, err
		}
		if n != len(chunk) {
			return total, io.ErrShortWrite
		}
		b = b[n:]
	}
	return total, nil
}

func (c *Client) httpStream(ctx context.Context, h handshake, confirmed *atomic.Uint64, start time.Time, buffer []byte) error {
	base := "?id=" + url.QueryEscape(h.ID)
	if h.Direction == "download" {
		streamStart := time.Now()
		r, err := c.request(ctx, "GET", "/api/download"+base+"&seconds="+strconv.FormatFloat(h.Seconds, 'f', 6, 64), nil)
		if err != nil {
			return err
		}
		defer r.Body.Close()
		if r.Header.Get("Content-Encoding") != "" {
			return errors.New("compressed measurement response")
		}
		for {
			n, err := r.Body.Read(buffer)
			confirmed.Add(uint64(n))
			if err == io.EOF {
				if time.Since(streamStart) < seconds(h.Seconds) {
					return errors.New("download stream ended before the measurement deadline")
				}
				return nil
			}
			if err != nil {
				return err
			}
		}
	}
	end := start.Add(seconds(h.Seconds))
	for time.Now().Before(end) {
		r, err := c.request(ctx, "POST", "/api/upload"+base, uploadBody{bytes.NewReader(c.payload)})
		if err != nil {
			return err
		}
		acknowledged, err := strconv.ParseUint(r.Header.Get("X-Received-Bytes"), 10, 64)
		_, drainErr := io.Copy(io.Discard, r.Body)
		r.Body.Close()
		if drainErr != nil {
			return drainErr
		}
		if err != nil || acknowledged != uint64(len(c.payload)) {
			return errors.New("upload acknowledgment mismatch")
		}
		confirmed.Add(acknowledged)
	}
	return nil
}
