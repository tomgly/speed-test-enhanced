package speedtest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	web "speedtest-enhanced"
)

func localServer(t *testing.T) *Server {
	t.Helper()
	s, err := NewServer(ServerConfig{HTTPAddress: "127.0.0.1:0", TCPAddress: "127.0.0.1:0", UDPAddress: "127.0.0.1:0"}, web.Assets)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		if err := s.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	return s
}
func TestHTTPAPI(t *testing.T) {
	s, err := NewServer(ServerConfig{CORSOrigin: "http://ui.test"}, web.Assets)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path string, body io.Reader) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, body)
		r.Header.Set("Origin", "http://ui.test")
		s.HTTP.Handler.ServeHTTP(w, r)
		return w
	}
	for _, path := range []string{"/", "/assets/js/measurement-worker.js", "/assets/css/speedtest.css", "/LICENSE", "/healthz", "/api/spec", "/api/connection"} {
		r := request("GET", path, nil)
		if r.Code != 200 {
			t.Fatal(path, r.Code)
		}
		if r.Header().Get("Access-Control-Allow-Origin") != "http://ui.test" {
			t.Fatal("CORS")
		}
	}
	for _, path := range []string{"/.git/config", "/go.mod", "/internal/speedtest/server.go", "/assets/../go.mod", "/downloading"} {
		if request("GET", path, nil).Code != 404 {
			t.Fatal(path)
		}
	}
	for _, tc := range []struct {
		method, path string
		code         int
	}{{"GET", "/api/sessions", 405}, {"HEAD", "/api/ping", 200}, {"OPTIONS", "/api/upload", 204}, {"POST", "/api/ping", 405}, {"GET", "/api/download?seconds=1", 403}} {
		if w := request(tc.method, tc.path, nil); w.Code != tc.code {
			t.Fatal(tc, w.Code)
		}
	}
	w := request("POST", "/api/sessions", nil)
	var sess SessionInfo
	if err = json.Unmarshal(w.Body.Bytes(), &sess); err != nil {
		t.Fatal(err)
	}
	p := "?id=" + sess.ID
	upload := request("POST", "/api/upload"+p, bytes.NewReader(make([]byte, 12345)))
	if upload.Code != 200 || upload.Header().Get("X-Received-Bytes") != "12345" {
		t.Fatal(upload)
	}
	for _, value := range []string{"NaN", "+Inf", "-1", "31"} {
		if request("GET", "/api/download"+p+"&seconds="+value, nil).Code != 400 {
			t.Fatal(value)
		}
	}
	if request("DELETE", "/api/session"+p, nil).Code != 204 || request("POST", "/api/upload"+p, strings.NewReader("x")).Code != 403 {
		t.Fatal("session cancellation")
	}
}
func TestTransfers(t *testing.T) {
	s := localServer(t)
	for _, protocol := range []string{"tcp", "udp", "http"} {
		for _, n := range []int{1, 4} {
			t.Run(protocol+string(rune('0'+n)), func(t *testing.T) {
				c := DefaultConfig()
				c.Protocol = protocol
				c.Mode = "manual"
				c.Connections = n
				c.Duration = .12
				c.Warmup = .06
				c.Rate = 20_000_000
				r, err := NewClient(s.Address()).Run(context.Background(), c, nil)
				if err != nil {
					t.Fatal(err)
				}
				if r.Client != "127.0.0.1" || r.Server != "127.0.0.1" || r.Ping < 0 || r.Jitter < 0 {
					t.Fatal(r)
				}
				for _, v := range []Transfer{r.Download, r.Upload} {
					if v.Bytes == 0 || v.Connections != n || v.Seconds < c.Duration || v.Seconds > 3 || math.Abs(v.BPS-float64(v.Bytes)*8/v.Seconds) > 1 {
						t.Fatal(v)
					}
					if protocol == "udp" && (v.SentPackets == 0 || v.ReceivedPackets+v.LostPackets != v.SentPackets || v.Bytes != v.ReceivedPackets*uint64(c.PacketSize-udpHeader)) {
						t.Fatal(v)
					}
				}
			})
		}
	}
}
func TestCancellation(t *testing.T) {
	s := localServer(t)
	for _, protocol := range []string{"tcp", "udp", "http"} {
		for _, direction := range []string{"download", "upload"} {
			t.Run(protocol+direction, func(t *testing.T) {
				c := NewClient(s.Address())
				if err := c.Open(context.Background()); err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				cfg := DefaultConfig()
				cfg.Protocol = protocol
				ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
				defer cancel()
				start := time.Now()
				r, err := c.Window(ctx, cfg, direction, 2, 2*time.Second, nil)
				if err == nil || r != (Transfer{}) || time.Since(start) > 3*time.Second {
					t.Fatal(r, err)
				}
			})
		}
	}
}
func TestSessionCapacityAndExpiration(t *testing.T) {
	s := localServer(t)
	id, err := s.createSession()
	if err != nil {
		t.Fatal(err)
	}
	releases := make([]func(), 0, 128)
	for i := 0; i < 128; i++ {
		_, release, err := s.acquire(id)
		if err != nil {
			t.Fatal(err)
		}
		releases = append(releases, release)
	}
	if _, _, err = s.acquire(id); err == nil {
		t.Fatal("unbounded session")
	}
	for _, release := range releases {
		release()
	}
	s.mu.Lock()
	s.sessions[id].expires = time.Now().Add(-time.Second)
	s.mu.Unlock()
	if _, _, err = s.acquire(id); err == nil {
		t.Fatal("expired session accepted")
	}
}
func TestNativeRejectsUnknownSession(t *testing.T) {
	s := localServer(t)
	h := handshake{ID: "invalid", Protocol: "tcp", Direction: "download", Seconds: .1, Rate: 1, PacketSize: 1400}
	if stream, err := prepareStream(context.Background(), s.TCPListener.Addr().String(), port(s.UDP.LocalAddr()), h); err == nil {
		stream.close()
		t.Fatal("invalid session accepted")
	}
}
func TestEarlyEOFRejected(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/sessions" {
			writeJSON(w, SessionInfo{ID: "test"})
			return
		}
		if r.URL.Path == "/api/download" {
			w.Write([]byte("short"))
			return
		}
		w.WriteHeader(204)
	}))
	defer s.Close()
	c := NewClient(s.URL)
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cfg := DefaultConfig()
	cfg.Protocol = "http"
	if _, err := c.Window(context.Background(), cfg, "download", 1, time.Second, nil); err == nil {
		t.Fatal("early EOF accepted")
	}
}

func TestAutomaticTransferFixesSelectedCount(t *testing.T) {
	s := localServer(t)
	cfg := DefaultConfig()
	cfg.Connections = 4
	cfg.Duration = .25
	cfg.Warmup = .05
	observed := map[string][]int{}
	result, err := NewClient(s.Address()).Run(context.Background(), cfg, func(p Progress) { observed[p.Phase] = append(observed[p.Phase], p.Connections) })
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range []struct {
		direction string
		r         Transfer
	}{{"download", result.Download}, {"upload", result.Upload}} {
		if v.r.Connections < 1 || v.r.Connections > 4 || len(observed[v.direction]) == 0 {
			t.Fatal(v)
		}
		for _, n := range observed[v.direction] {
			if n != v.r.Connections {
				t.Fatal("changed count", n, v.r.Connections)
			}
		}
	}
}
func TestIPv6Native(t *testing.T) {
	s, err := NewServer(ServerConfig{HTTPAddress: "[::1]:0", TCPAddress: "[::1]:0", UDPAddress: "[::1]:0"}, web.Assets)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.Start(); err != nil {
		t.Skipf("IPv6 loopback unavailable: %v", err)
	}
	defer s.Close(context.Background())
	for _, protocol := range []string{"tcp", "udp"} {
		c := DefaultConfig()
		c.Protocol = protocol
		c.Mode = "manual"
		c.Connections = 1
		c.Duration = .06
		c.Warmup = 0
		c.Rate = 10_000_000
		r, err := NewClient(s.Address()).Run(context.Background(), c, nil)
		if err != nil {
			t.Fatal(protocol, err)
		}
		if r.Client != "::1" || r.Server != "::1" {
			t.Fatal(r.Client, r.Server)
		}
	}
}
