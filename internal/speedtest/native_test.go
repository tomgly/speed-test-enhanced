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

func TestUDPDownloadRejectsReceiveFailure(t *testing.T) {
	client, peer := tcpPair(t)
	listener, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	u, err := net.DialUDP("udp", nil, listener.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer u.Close()
	s := &nativeStream{c: client, udp: u}
	control := make(chan error, 1)
	go func() {
		b := make([]byte, 1400)
		binary.BigEndian.PutUint64(b[16:24], 1)
		if _, err := listener.WriteToUDP(b, u.LocalAddr().(*net.UDPAddr)); err != nil {
			control <- err
			peer.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
		u.Close()
		time.Sleep(100 * time.Millisecond)
		control <- writeLine(peer, packetStats{Sent: 1})
		peer.Close()
	}()
	var confirmed atomic.Uint64
	_, err = s.runUDP(context.Background(), handshake{Protocol: "udp", Direction: "download", Seconds: .1, PacketSize: 1400}, &confirmed)
	if controlErr := <-control; controlErr != nil {
		t.Fatal(controlErr)
	}
	if err == nil {
		t.Fatal("UDP receive failure accepted as packet loss")
	}
}

func TestUDPListenerFailureClosesActiveControl(t *testing.T) {
	s := localServer(t)
	c := NewClient(s.Address())
	if err := c.Open(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	h := handshake{ID: c.session.ID, Protocol: "udp", Direction: "upload", Seconds: .1, Rate: 1_000_000, PacketSize: 1400}
	stream, err := prepareStream(context.Background(), s.TCPListener.Addr().String(), port(s.UDP.LocalAddr()), h)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.close()
	s.UDP.Close()
	stream.c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	_, err = stream.c.Read(b[:])
	if err != io.EOF {
		t.Fatal("failed UDP listener left control open", err)
	}
}
