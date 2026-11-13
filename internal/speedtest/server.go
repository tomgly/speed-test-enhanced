package speedtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type ServerConfig struct {
	HTTPAddress, TCPAddress, UDPAddress string
	IPv6Origin, IPv4Origin, CORSOrigin  string
}

const downloadEnd = "speedtest-end-v1"

type session struct {
	ctx     context.Context
	cancel  context.CancelFunc
	expires time.Time
	active  int
	timer   *time.Timer
}
type Server struct {
	HTTP                      *http.Server
	HTTPListener, TCPListener net.Listener
	UDP                       *net.UDPConn
	config                    ServerConfig
	assets                    fs.FS
	payload                   []byte
	buffers                   sync.Pool
	mu                        sync.Mutex
	sessions                  map[string]*session
	peers                     map[[16]byte]*udpPeer
	connections               map[net.Conn]bool
	active                    int
	closed                    bool
	started                   time.Time
	wg                        sync.WaitGroup
}
type SessionInfo struct {
	ID      string `json:"id"`
	TCPPort int    `json:"tcpPort"`
	UDPPort int    `json:"udpPort"`
	Client  string `json:"client"`
	Server  string `json:"server"`
}

func NewServer(config ServerConfig, assets fs.FS) (*Server, error) {
	p := make([]byte, BufferSize)
	if _, err := rand.Read(p); err != nil {
		return nil, err
	}
	s := &Server{config: config, assets: assets, payload: p, sessions: make(map[string]*session), peers: make(map[[16]byte]*udpPeer), connections: make(map[net.Conn]bool)}
	s.started = time.Now()
	s.buffers.New = func() any { b := make([]byte, BufferSize); return &b }
	s.HTTP = &http.Server{Handler: http.HandlerFunc(s.serveHTTP), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 40 * time.Second, WriteTimeout: 40 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 * 1024}
	return s, nil
}
func (s *Server) Start() error {
	var err error
	s.HTTPListener, err = net.Listen("tcp", s.config.HTTPAddress)
	if err != nil {
		return err
	}
	s.TCPListener, err = net.Listen("tcp", s.config.TCPAddress)
	if err != nil {
		s.HTTPListener.Close()
		return err
	}
	addr, err := net.ResolveUDPAddr("udp", s.config.UDPAddress)
	if err == nil {
		s.UDP, err = net.ListenUDP("udp", addr)
	}
	if err != nil {
		s.HTTPListener.Close()
		s.TCPListener.Close()
		return err
	}
	s.UDP.SetReadBuffer(4 * 1024 * 1024)
	s.UDP.SetWriteBuffer(4 * 1024 * 1024)
	s.wg.Add(3)
	go func() { defer s.wg.Done(); s.HTTP.Serve(s.HTTPListener) }()
	go func() { defer s.wg.Done(); s.acceptTCP() }()
	go func() { defer s.wg.Done(); s.receiveUDP() }()
	return nil
}
func (s *Server) Close(ctx context.Context) error {
	s.mu.Lock()
	s.closed = true
	for _, v := range s.sessions {
		v.cancel()
		if v.timer != nil {
			v.timer.Stop()
		}
	}
	for c := range s.connections {
		c.Close()
	}
	s.mu.Unlock()
	if s.TCPListener != nil {
		s.TCPListener.Close()
	}
	if s.UDP != nil {
		s.UDP.Close()
	}
	err := s.HTTP.Shutdown(ctx)
	if err != nil {
		s.HTTP.Close()
	}
	s.wg.Wait()
	return err
}
func port(a net.Addr) int {
	_, p, _ := net.SplitHostPort(a.String())
	n, _ := strconv.Atoi(p)
	return n
}
func host(a string) string {
	h, _, err := net.SplitHostPort(a)
	if err != nil {
		return a
	}
	return strings.TrimPrefix(h, "::ffff:")
}
func (s *Server) createSession() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range s.sessions {
		if time.Now().After(v.expires) {
			v.cancel()
			if v.timer != nil {
				v.timer.Stop()
			}
			delete(s.sessions, id)
		}
	}
	if s.closed || len(s.sessions) >= 512 {
		return "", errors.New("server session capacity reached")
	}
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	id := hex.EncodeToString(b)
	ctx, cancel := context.WithCancel(context.Background())
	ctx = context.WithValue(ctx, byteCountersKey{}, &byteCounters{})
	v := &session{ctx: ctx, cancel: cancel, expires: time.Now().Add(5 * time.Minute)}
	s.sessions[id] = v
	// Expire even when there are no further HTTP requests.
	v.timer = time.AfterFunc(5*time.Minute, func() {
		s.mu.Lock()
		if s.sessions[id] == v {
			v.cancel()
			delete(s.sessions, id)
		}
		s.mu.Unlock()
	})
	return id, nil
}
func (s *Server) acquire(id string) (context.Context, func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.sessions[id]
	if v == nil || s.closed || time.Now().After(v.expires) || v.ctx.Err() != nil {
		return nil, nil, errors.New("invalid or expired session")
	}
	if v.active >= 128 || s.active >= 512 {
		return nil, nil, errors.New("transfer capacity reached")
	}
	v.active++
	s.active++
	return v.ctx, func() { s.mu.Lock(); v.active--; s.active--; s.mu.Unlock() }, nil
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(v)
}
func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store, no-transform")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Speedtest-Client", host(r.RemoteAddr))
	if a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		w.Header().Set("X-Speedtest-Server", host(a.String()))
	}
	w.Header().Set("Vary", "Origin")
	if s.config.CORSOrigin != "" && r.Header.Get("Origin") == s.config.CORSOrigin {
		w.Header().Set("Access-Control-Allow-Origin", s.config.CORSOrigin)
		w.Header().Set("Access-Control-Allow-Methods", "GET, HEAD, POST, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		w.Header().Set("Access-Control-Expose-Headers", "X-Speedtest-Client, X-Speedtest-Server, X-Received-Bytes, X-Speedtest-Download")
	}
	if r.Method == "OPTIONS" {
		w.WriteHeader(204)
		return
	}
	switch r.URL.Path {
	case "/api/spec":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		writeJSON(w, map[string]any{"probeSeconds": ProbeDuration.Seconds(), "probeSamples": ProbeSamples, "gain": GainThreshold, "factor": ProbeFactor, "warmup": 1, "pingSamples": 10, "maxConnections": 128})
		return
	case "/healthz":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		io.WriteString(w, "ok")
		return
	case "/api/connection":
		if r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		writeJSON(w, map[string]string{"ipv6Origin": s.config.IPv6Origin, "ipv4Origin": s.config.IPv4Origin})
		return
	case "/api/ping":
		if r.Method != "HEAD" && r.Method != "GET" {
			w.WriteHeader(405)
			return
		}
		w.Header().Set("Content-Length", "0")
		return
	case "/api/sessions":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		id, err := s.createSession()
		if err != nil {
			http.Error(w, err.Error(), 503)
			return
		}
		tcp, udp := 0, 0
		if s.TCPListener != nil {
			tcp = port(s.TCPListener.Addr())
		}
		if s.UDP != nil {
			udp = port(s.UDP.LocalAddr())
		}
		writeJSON(w, SessionInfo{id, tcp, udp, w.Header().Get("X-Speedtest-Client"), w.Header().Get("X-Speedtest-Server")})
		return
	case "/api/session":
		if r.Method != "DELETE" {
			w.WriteHeader(405)
			return
		}
		s.mu.Lock()
		id := r.URL.Query().Get("id")
		if v := s.sessions[id]; v != nil {
			v.cancel()
			if v.timer != nil {
				v.timer.Stop()
			}
			delete(s.sessions, id)
		}
		s.mu.Unlock()
		w.WriteHeader(204)
		return
	case "/api/results":
		if r.Method != "POST" {
			w.WriteHeader(405)
			return
		}
		ctx, release, err := s.acquire(r.URL.Query().Get("id"))
		if err != nil {
			http.Error(w, err.Error(), 403)
			return
		}
		defer release()
		var result Result
		if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&result); err != nil {
			http.Error(w, "invalid result JSON", 400)
			return
		}
		counts, _ := ctx.Value(byteCountersKey{}).(*byteCounters)
		if err = ValidateResult(result, counts); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSON(w, map[string]bool{"valid": true})
		return
	case "/api/download", "/api/upload":
		direction := "download"
		method := "GET"
		if r.URL.Path == "/api/upload" {
			direction = "upload"
			method = "POST"
		}
		if r.Method != method {
			w.WriteHeader(405)
			return
		}
		ctx, release, err := s.acquire(r.URL.Query().Get("id"))
		if err != nil {
			http.Error(w, err.Error(), 403)
			return
		}
		defer release()
		deadlineDone := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(deadlineDone)
			http.NewResponseController(w).SetReadDeadline(time.Now())
			http.NewResponseController(w).SetWriteDeadline(time.Now())
		})
		defer func() {
			if !stop() {
				<-deadlineDone
			}
		}()
		if direction == "download" {
			duration, err := strconv.ParseFloat(r.URL.Query().Get("seconds"), 64)
			if err != nil || !(duration >= .05 && duration <= 30) {
				http.Error(w, "invalid duration", 400)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			framed := r.URL.Query().Get("framing") == "1"
			if framed {
				w.Header().Set("X-Speedtest-Download", "framed-v1")
			}
			end := time.Now().Add(seconds(duration))
			for time.Now().Before(end) && ctx.Err() == nil && r.Context().Err() == nil {
				n, err := w.Write(s.payload)
				recordBytes(ctx, "download", uint64(n))
				if err != nil {
					return
				}
			}
			if framed && ctx.Err() == nil && r.Context().Err() == nil {
				io.WriteString(w, downloadEnd)
			}
		} else {
			if r.ContentLength > 32*1024*1024 {
				http.Error(w, "body too large", 413)
				return
			}
			b := s.buffers.Get().(*[]byte)
			defer s.buffers.Put(b)
			var n int64
			body := http.MaxBytesReader(w, r.Body, 32*1024*1024)
			var err error
			for {
				var count int
				count, err = body.Read(*b)
				n += int64(count)
				if err != nil {
					break
				}
			}
			if err == io.EOF {
				err = nil
			}
			if err != nil {
				http.Error(w, "incomplete or oversized upload", 400)
				return
			}
			w.Header().Set("X-Received-Bytes", strconv.FormatInt(n, 10))
			recordBytes(ctx, "upload", uint64(n))
			w.Header().Set("Content-Length", "0")
		}
		return
	}
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if !fs.ValidPath(path) || (path != "index.html" && path != "LICENSE" && !strings.HasPrefix(path, "assets/")) {
		http.NotFound(w, r)
		return
	}
	if s.assets == nil {
		http.NotFound(w, r)
		return
	}
	f, err := s.assets.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil || stat.IsDir() {
		http.NotFound(w, r)
		return
	}
	data, ok := f.(io.ReadSeeker)
	if !ok {
		http.Error(w, "asset unavailable", 500)
		return
	}
	http.ServeContent(w, r, path, time.Time{}, data)
}
func (s *Server) acceptTCP() {
	for {
		c, err := s.TCPListener.Accept()
		if err != nil {
			return
		}
		s.mu.Lock()
		if s.closed || len(s.connections) >= 512 {
			s.mu.Unlock()
			c.Close()
			continue
		}
		s.connections[c] = true
		s.wg.Add(1)
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			defer c.Close()
			defer func() { s.mu.Lock(); delete(s.connections, c); s.mu.Unlock() }()
			s.handleNative(c)
		}()
	}
}
func (s *Server) Address() string { return fmt.Sprintf("http://%s", s.HTTPListener.Addr()) }
