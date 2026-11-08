package speedtest

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"
)

type handshake struct {
	ID         string  `json:"id"`
	Protocol   string  `json:"protocol"`
	Direction  string  `json:"direction"`
	Seconds    float64 `json:"seconds"`
	Rate       uint64  `json:"rate"`
	PacketSize int     `json:"packetSize"`
}
type ready struct {
	Key   string `json:"key,omitempty"`
	Error string `json:"error,omitempty"`
}

// Read exactly a bounded JSON line without buffering any following payload.
func readLine(r io.Reader, v any) error {
	b := make([]byte, 0, 256)
	one := []byte{0}
	for len(b) < 4096 {
		if _, err := io.ReadFull(r, one); err != nil {
			return err
		}
		if one[0] == '\n' {
			return json.Unmarshal(b, v)
		}
		b = append(b, one[0])
	}
	return errors.New("control message too large")
}
func writeLine(w io.Writer, v any) error { return json.NewEncoder(w).Encode(v) }
func startByte(c net.Conn) error {
	var b [1]byte
	_, err := io.ReadFull(c, b[:])
	if err == nil && b[0] != 1 {
		return errors.New("invalid start marker")
	}
	return err
}

func (s *Server) handleNative(c net.Conn) {
	c.SetDeadline(time.Now().Add(5 * time.Second))
	var h handshake
	if err := readLine(c, &h); err != nil {
		return
	}
	valid := Config{h.Protocol, "manual", 1, h.Seconds, 0, h.Rate, h.PacketSize}
	if err := valid.Validate(); err != nil || h.Protocol == "http" || (h.Direction != "download" && h.Direction != "upload") {
		writeLine(c, ready{Error: "invalid transfer configuration"})
		return
	}
	ctx, release, err := s.acquire(h.ID)
	if err != nil {
		writeLine(c, ready{Error: err.Error()})
		return
	}
	defer release()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	c.SetDeadline(time.Now().Add(seconds(h.Seconds) + 8*time.Second))
	if h.Protocol == "udp" {
		s.handleUDP(ctx, c, h)
		return
	}
	if err := writeLine(c, ready{}); err != nil {
		return
	}
	if startByte(c) != nil {
		return
	}
	if h.Direction == "download" {
		end := time.Now().Add(seconds(h.Seconds))
		for time.Now().Before(end) {
			n, err := c.Write(s.payload)
			recordBytes(ctx, "download", uint64(n))
			if err != nil {
				return
			}
		}
	} else {
		b := s.buffers.Get().(*[]byte)
		defer s.buffers.Put(b)
		var received atomic.Uint64
		done := make(chan struct{})
		acks := make(chan struct{})
		go func() {
			defer close(acks)
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			var buf [8]byte
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					binary.BigEndian.PutUint64(buf[:], received.Load())
					if _, err := c.Write(buf[:]); err != nil {
						c.Close()
						return
					}
				}
			}
		}()
		for {
			n, e := c.Read(*b)
			if n > 0 {
				received.Add(uint64(n))
				recordBytes(ctx, "upload", uint64(n))
			}
			if e != nil {
				close(done)
				<-acks
				if e == io.EOF {
					var ack [8]byte
					binary.BigEndian.PutUint64(ack[:], received.Load())
					c.Write(ack[:])
				}
				return
			}
		}
	}
}

// UDP payload carries a stream capability, sequence number, and monotonic sender
// offset. The receiver accepts each sequence once within a bounded reorder window.
const udpHeader = 32

type packetStats struct {
	Bytes         uint64  `json:"bytes"`
	Packets       uint64  `json:"packets"`
	Sent          uint64  `json:"sent"`
	Duplicates    uint64  `json:"duplicates"`
	Reordered     uint64  `json:"reordered"`
	Jitter        float64 `json:"jitter"`
	JitterSamples uint64  `json:"jitterSamples,omitempty"`
	Final         bool    `json:"final,omitempty"`
}
type packetTracker struct {
	packetStats
	highest         uint64
	seen            [1024]uint64
	previousArrival int64
	previousSent    int64
	differences     uint64
}

func (s *packetTracker) observe(seq uint64, sent int64, n int, now int64) {
	if seq == 0 {
		return
	}
	const window = 65536
	if seq <= s.highest && s.highest-seq >= window {
		return
	}
	if seq > s.highest {
		gap := seq - s.highest
		if gap >= window {
			clear(s.seen[:])
		} else {
			for i := uint64(1); i <= gap; i++ {
				at := (s.highest + i) % window
				s.seen[at/64] &^= uint64(1) << (at % 64)
			}
		}
	}
	at := seq % window
	mask := uint64(1) << (at % 64)
	if s.seen[at/64]&mask != 0 {
		s.Duplicates++
		return
	}
	s.seen[at/64] |= mask
	if seq < s.highest {
		s.Reordered++
	}
	s.highest = max(s.highest, seq)
	s.Bytes += uint64(n)
	s.Packets++
	if seq < s.highest {
		return
	}
	if s.previousArrival != 0 && sent >= s.previousSent {
		delta := math.Abs(float64((now-s.previousArrival)-(sent-s.previousSent))) / 1e6
		s.differences++
		s.JitterSamples = s.differences
		s.Jitter += (delta - s.Jitter) / float64(s.differences)
	}
	s.previousArrival = now
	s.previousSent = sent
}

type udpPeer struct {
	mu     sync.Mutex
	addr   netip.AddrPort
	ip     netip.Addr
	ready  chan struct{}
	upload bool
	active bool
	stats  packetTracker
	size   int
}

func (s *Server) receiveUDP() {
	b := make([]byte, 65536)
	for {
		n, addr, err := s.UDP.ReadFromUDPAddrPort(b)
		if err != nil {
			return
		}
		if n < udpHeader {
			continue
		}
		var key [16]byte
		copy(key[:], b[:16])
		s.mu.Lock()
		p := s.peers[key]
		s.mu.Unlock()
		if p == nil {
			continue
		}
		p.mu.Lock()
		if p.ip != addr.Addr().Unmap() {
			p.mu.Unlock()
			continue
		}
		seq := binary.BigEndian.Uint64(b[16:24])
		if !p.addr.IsValid() && seq == 0 {
			p.addr = addr
			close(p.ready)
		}
		if p.upload && p.active && p.addr == addr && n == p.size {
			p.stats.observe(seq, int64(binary.BigEndian.Uint64(b[24:32])), n-udpHeader, time.Since(s.started).Nanoseconds())
		}
		p.mu.Unlock()
	}
}
func (s *Server) handleUDP(ctx context.Context, c net.Conn, h handshake) {
	var key [16]byte
	if _, err := rand.Read(key[:]); err != nil {
		return
	}
	ip, err := netip.ParseAddr(host(c.RemoteAddr().String()))
	if err != nil {
		return
	}
	p := &udpPeer{ip: ip.Unmap(), ready: make(chan struct{}), upload: h.Direction == "upload", size: h.PacketSize}
	s.mu.Lock()
	s.peers[key] = p
	s.mu.Unlock()
	defer func() { s.mu.Lock(); delete(s.peers, key); s.mu.Unlock() }()
	if writeLine(c, ready{Key: hex.EncodeToString(key[:])}) != nil {
		return
	}
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	select {
	case <-p.ready:
	case <-ctx.Done():
		return
	case <-timer.C:
		return
	}
	p.mu.Lock()
	p.active = true
	addr := p.addr
	p.mu.Unlock()
	if writeLine(c, ready{}) != nil || startByte(c) != nil {
		return
	}
	if h.Direction == "download" {
		sent, err := sendUDP(ctx, h.Seconds, h.Rate, h.PacketSize, key, func(b []byte) (int, error) { return s.UDP.WriteToUDPAddrPort(b, addr) })
		if err == nil {
			recordBytes(ctx, "download", sent*uint64(h.PacketSize-udpHeader))
			writeLine(c, packetStats{Sent: sent})
		}
	} else {
		done := make(chan struct{})
		acks := make(chan struct{})
		go func() {
			defer close(acks)
			ticker := time.NewTicker(200 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-done:
					return
				case <-ticker.C:
					p.mu.Lock()
					r := p.stats.packetStats
					p.mu.Unlock()
					if writeLine(c, r) != nil {
						c.Close()
						return
					}
				}
			}
		}()
		var sent packetStats
		if readLine(c, &sent) != nil {
			close(done)
			<-acks
			return
		}
		time.Sleep(100 * time.Millisecond) // Bounded tail drain, included in elapsed time.
		p.mu.Lock()
		p.active = false
		p.stats.Sent = sent.Sent
		result := p.stats.packetStats
		result.Final = true
		p.mu.Unlock()
		recordBytes(ctx, "upload", result.Bytes)
		close(done)
		<-acks
		writeLine(c, &result)
	}
}

// Pace bounded batches against elapsed monotonic time; never build an unbounded
// catch-up burst after a scheduler pause. Rate is payload bps (header excluded).
func sendUDP(ctx context.Context, duration float64, rate uint64, size int, key [16]byte, write func([]byte) (int, error)) (uint64, error) {
	b := make([]byte, size)
	rand.Read(b)
	copy(b, key[:])
	payload := size - udpHeader
	start := time.Now()
	end := start.Add(seconds(duration))
	seq := uint64(0)
	interval := float64(payload) * 8 / float64(rate) * float64(time.Second)
	batch := max(1, min(64, int(200_000/max(1, interval))))
	next := start
	for time.Now().Before(end) {
		if err := ctx.Err(); err != nil {
			return seq, err
		}
		now := time.Now()
		if next.After(now) {
			t := time.NewTimer(time.Until(minTime(next, end)))
			select {
			case <-ctx.Done():
				t.Stop()
				return seq, ctx.Err()
			case <-t.C:
			}
		}
		if !time.Now().Before(end) {
			break
		}
		for i := 0; i < batch && time.Now().Before(end); i++ {
			binary.BigEndian.PutUint64(b[16:24], seq+1)
			binary.BigEndian.PutUint64(b[24:32], uint64(time.Since(start)))
			n, err := write(b)
			if err != nil {
				return seq, err
			}
			if n != len(b) {
				return seq, io.ErrShortWrite
			}
			seq++
		}
		next = next.Add(time.Duration(interval * float64(batch)))
		if time.Since(next) > time.Millisecond {
			next = time.Now()
		}
	}
	return seq, nil
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

type nativeStream struct {
	c   *net.TCPConn
	udp *net.UDPConn
	key [16]byte
}

func prepareStream(ctx context.Context, address string, udpPort int, h handshake) (*nativeStream, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	s := &nativeStream{c: conn.(*net.TCPConn)}
	stop := context.AfterFunc(ctx, func() { s.c.Close() })
	defer stop()
	ok := false
	defer func() {
		if !ok {
			s.close()
		}
	}()
	s.c.SetDeadline(time.Now().Add(seconds(h.Seconds) + 8*time.Second))
	if err = writeLine(s.c, h); err != nil {
		return nil, err
	}
	var r ready
	if err = readLine(s.c, &r); err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, errors.New(r.Error)
	}
	if h.Protocol == "udp" {
		key, e := hex.DecodeString(r.Key)
		if e != nil || len(key) != 16 {
			return nil, errors.New("invalid UDP capability")
		}
		copy(s.key[:], key)
		a := s.c.RemoteAddr().(*net.TCPAddr)
		s.udp, err = net.DialUDP("udp", nil, &net.UDPAddr{IP: a.IP, Port: udpPort, Zone: a.Zone})
		if err != nil {
			return nil, err
		}
		s.udp.SetReadBuffer(4 * 1024 * 1024)
		s.udp.SetWriteBuffer(4 * 1024 * 1024)
		hello := make([]byte, udpHeader)
		copy(hello, s.key[:])
		if _, err = s.udp.Write(hello); err != nil {
			return nil, err
		}
		if err = readLine(s.c, &r); err != nil {
			return nil, err
		}
	}
	ok = true
	return s, nil
}
func (s *nativeStream) close() {
	s.c.Close()
	if s.udp != nil {
		s.udp.Close()
	}
}

func (s *nativeStream) run(ctx context.Context, h handshake, confirmed *atomic.Uint64) (packetStats, error) {
	stop := context.AfterFunc(ctx, s.close)
	defer stop()
	if _, err := s.c.Write([]byte{1}); err != nil {
		return packetStats{}, err
	}
	if h.Protocol == "udp" {
		return s.runUDP(ctx, h, confirmed)
	}
	if h.Direction == "download" {
		b := make([]byte, BufferSize)
		total := uint64(0)
		for {
			n, err := s.c.Read(b)
			if n > 0 {
				total += uint64(n)
				confirmed.Add(uint64(n))
			}
			if err == io.EOF {
				return packetStats{Bytes: total}, nil
			}
			if err != nil {
				return packetStats{}, err
			}
		}
	}
	var acknowledged atomic.Uint64
	readerErr := make(chan error, 1)
	go func() {
		var b [8]byte
		for {
			_, err := io.ReadFull(s.c, b[:])
			if err != nil {
				if err == io.EOF {
					err = nil
				}
				readerErr <- err
				return
			}
			n := binary.BigEndian.Uint64(b[:])
			old := acknowledged.Swap(n)
			if n < old {
				readerErr <- errors.New("invalid acknowledgment")
				s.c.Close()
				return
			}
			confirmed.Add(n - old)
		}
	}()
	b := make([]byte, BufferSize)
	rand.Read(b)
	end := time.Now().Add(seconds(h.Seconds))
	sent := uint64(0)
	var sendErr error
	for time.Now().Before(end) {
		n, err := s.c.Write(b)
		sent += uint64(n)
		if err != nil {
			sendErr = err
			break
		}
	}
	s.c.CloseWrite()
	err := <-readerErr
	if sendErr != nil {
		return packetStats{}, sendErr
	}
	if err != nil {
		return packetStats{}, err
	}
	if acknowledged.Load() != sent {
		return packetStats{}, errors.New("upload acknowledgment mismatch")
	}
	return packetStats{Bytes: sent}, nil
}
func (s *nativeStream) runUDP(ctx context.Context, h handshake, confirmed *atomic.Uint64) (packetStats, error) {
	if h.Direction == "upload" {
		type answer struct {
			stats packetStats
			err   error
		}
		answerCh := make(chan answer, 1)
		go func() {
			last := uint64(0)
			for {
				var r packetStats
				err := readLine(s.c, &r)
				if err != nil {
					answerCh <- answer{err: err}
					return
				}
				if r.Bytes < last {
					answerCh <- answer{err: errors.New("invalid UDP acknowledgment")}
					s.c.Close()
					return
				}
				confirmed.Add(r.Bytes - last)
				last = r.Bytes
				if r.Final {
					answerCh <- answer{stats: r}
					return
				}
			}
		}()
		sent, err := sendUDP(ctx, h.Seconds, h.Rate, h.PacketSize, s.key, s.udp.Write)
		if err != nil {
			s.c.Close()
			<-answerCh
			return packetStats{}, err
		}
		if err = writeLine(s.c, packetStats{Sent: sent}); err != nil {
			s.c.Close()
			<-answerCh
			return packetStats{}, err
		}
		a := <-answerCh
		if a.err == nil && (a.stats.Sent != sent || a.stats.Packets > sent || a.stats.Bytes != a.stats.Packets*uint64(h.PacketSize-udpHeader)) {
			return packetStats{}, errors.New("invalid UDP receiver result")
		}
		return a.stats, a.err
	}
	var mu sync.Mutex
	var stats packetTracker
	arrivalEpoch := time.Now()
	done := make(chan error, 1)
	s.udp.SetReadDeadline(time.Now().Add(seconds(h.Seconds) + 5*time.Second))
	go func() {
		b := make([]byte, 65536)
		for {
			n, err := s.udp.Read(b)
			if err != nil {
				done <- err
				return
			}
			if n < udpHeader || string(b[:16]) != string(s.key[:]) {
				continue
			}
			mu.Lock()
			old := stats.Bytes
			stats.observe(binary.BigEndian.Uint64(b[16:24]), int64(binary.BigEndian.Uint64(b[24:32])), n-udpHeader, time.Since(arrivalEpoch).Nanoseconds())
			confirmed.Add(stats.Bytes - old)
			mu.Unlock()
		}
	}()
	// Buffered JSON is safe here: the control channel carries no binary payload.
	var sender packetStats
	err := json.NewDecoder(bufio.NewReader(s.c)).Decode(&sender)
	if err != nil {
		s.udp.Close()
		<-done
		return packetStats{}, err
	}
	s.udp.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
	<-done
	mu.Lock()
	defer mu.Unlock()
	stats.Sent = sender.Sent
	return stats.packetStats, nil
}
