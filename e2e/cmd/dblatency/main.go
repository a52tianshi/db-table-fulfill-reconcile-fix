// Command dblatency is a TCP proxy that adds artificial round-trip latency in
// front of MySQL, so the reconciliation/migration flow can be exercised under
// production-like network conditions (extra ~20ms per DB round trip) instead of
// a loopback connection with sub-millisecond latency.
//
// Usage:
//
//	go run ./cmd/dblatency -listen 127.0.0.1:3307 -target 127.0.0.1:3306 -rtt 20ms
//
// The delay is applied per direction (rtt/2 each way) to every TCP segment, so
// a single small query costs about one extra RTT; large result sets pay one
// delay per segment, which is what a real WAN link does.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"sync"
	"time"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:3307", "address to listen on")
	target := flag.String("target", "127.0.0.1:3306", "MySQL address to proxy to")
	rtt := flag.Duration("rtt", 20*time.Millisecond, "added round-trip latency (split across both directions)")
	jitter := flag.Duration("jitter", 0, "random extra latency added per direction, uniform in [0,jitter)")
	flag.Parse()

	oneWay := *rtt / 2
	log.Printf("dblatency listen=%s target=%s rtt=%s (one-way %s) jitter=%s",
		*listen, *target, *rtt, oneWay, *jitter)

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatalf("listen: %v", err)
	}
	for {
		client, err := ln.Accept()
		if err != nil {
			log.Fatalf("accept: %v", err)
		}
		go func() {
			server, err := net.Dial("tcp", *target)
			if err != nil {
				client.Close()
				log.Printf("dial %s: %v", *target, err)
				return
			}
			proxyConn(client, server, oneWay, *jitter)
		}()
	}
}

func proxyConn(client, server net.Conn, oneWay, jitter time.Duration) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		defer server.Close()
		copyDelayed(server, client, oneWay, jitter)
	}()
	go func() {
		defer wg.Done()
		defer client.Close()
		copyDelayed(client, server, oneWay, jitter)
	}()
	wg.Wait()
}

// copyDelayed forwards chunks from src to dst, sleeping before each write so the
// peer observes the configured per-direction delay.
func copyDelayed(dst, src net.Conn, delay, jitter time.Duration) {
	buf := make([]byte, 32*1024)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			d := delay
			if jitter > 0 {
				d += time.Duration(rand.Int63n(int64(jitter)))
			}
			if d > 0 {
				time.Sleep(d)
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return
			}
		}
		if err != nil {
			if err != io.EOF {
				return
			}
			return
		}
	}
}

var _ = fmt.Sprintf
