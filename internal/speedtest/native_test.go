package speedtest

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	l, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	client, err := net.DialTCP("tcp", nil, l.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	peer, err := l.AcceptTCP()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { peer.Close() })
	client.SetDeadline(time.Now().Add(3 * time.Second))
	peer.SetDeadline(time.Now().Add(3 * time.Second))
	return client, peer
}

func TestNativeDownloadRejectsEarlyEOF(t *testing.T) {
	client, peer := tcpPair(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer peer.Close()
		if startByte(peer) == nil {
			peer.Write([]byte("short"))
		}
	}()
	var confirmed atomic.Uint64
	s := &nativeStream{c: client}
	h := handshake{Protocol: "tcp", Direction: "download", Seconds: .1}
	_, err := s.run(context.Background(), h, &confirmed, make([]byte, BufferSize))
	<-done
	if err == nil {
		t.Fatal("early stream EOF accepted")
	}
}

func TestNativeUploadChecksFinalReceipt(t *testing.T) {
	for _, receipt := range []string{"valid", "missing", "oversized", "decreasing", "truncated"} {
		t.Run(receipt, func(t *testing.T) {
			client, peer := tcpPair(t)
			done := make(chan uint64, 1)
			go func() {
				defer peer.Close()
				if startByte(peer) != nil {
					done <- 0
					return
				}
				n, _ := io.Copy(io.Discard, peer)
				var ack [8]byte
				value := uint64(n)
				if receipt == "oversized" {
					value++
				}
				binary.BigEndian.PutUint64(ack[:], value)
				switch receipt {
				case "missing":
				case "truncated":
					peer.Write(ack[:4])
				default:
					peer.Write(ack[:])
					if receipt == "decreasing" {
						binary.BigEndian.PutUint64(ack[:], value-1)
						peer.Write(ack[:])
					}
				}
				done <- uint64(n)
			}()
			var confirmed atomic.Uint64
			s := &nativeStream{c: client}
			h := handshake{Protocol: "tcp", Direction: "upload", Seconds: .05}
			stats, err := s.run(context.Background(), h, &confirmed, make([]byte, BufferSize))
			received := <-done
			if receipt == "valid" {
				if err != nil || received == 0 || stats.Bytes != received || confirmed.Load() != received {
					t.Fatal(stats, confirmed.Load(), received, err)
				}
			} else if err == nil {
				t.Fatal("invalid final receipt accepted", receipt)
			}
		})
	}
}
