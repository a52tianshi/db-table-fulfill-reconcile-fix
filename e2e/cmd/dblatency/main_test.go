package main

import (
	"io"
	"net"
	"testing"
	"time"
)

// When a MySQL client disconnects abruptly (e.g. context cancellation or
// SIGTERM during a transaction), dblatency must close the backend connection to
// MySQL so MySQL rolls back the open transaction and releases row/metadata/advisory
// locks instead of keeping the backend session in Sleep state forever.
func TestProxyConnClosesBackendOnClientDisconnect(t *testing.T) {
	backendLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer backendLn.Close()

	proxyLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyLn.Close()

	backendEOF := make(chan error, 1)
	go func() {
		srvConn, err := backendLn.Accept()
		if err != nil {
			backendEOF <- err
			return
		}
		defer srvConn.Close()
		buf := make([]byte, 64)
		// Read initial query packet from client.
		if _, err := srvConn.Read(buf); err != nil {
			backendEOF <- err
			return
		}
		// Wait for client disconnect to propagate as EOF to backend.
		_, err = srvConn.Read(buf)
		backendEOF <- err
	}()

	proxyDone := make(chan struct{})
	go func() {
		defer close(proxyDone)
		c, err := proxyLn.Accept()
		if err != nil {
			return
		}
		s, err := net.Dial("tcp", backendLn.Addr().String())
		if err != nil {
			c.Close()
			return
		}
		proxyConn(c, s, 5*time.Millisecond, 0)
	}()

	client, err := net.Dial("tcp", proxyLn.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Write([]byte("BEGIN")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	// Abruptly close client connection (simulating ctx cancel / process kill).
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-backendEOF:
		if err != io.EOF {
			t.Fatalf("expected backend to observe io.EOF, got %v", err)
		}
	case <-time.After(300 * time.Millisecond):
		// Unblock the leaked backend goroutine before failing the test.
		backendLn.Close()
		t.Fatal("dblatency leaked backend connection after client disconnect: backend never received EOF")
	}

	select {
	case <-proxyDone:
	case <-time.After(300 * time.Millisecond):
		t.Fatal("proxyConn goroutine did not exit after client disconnect")
	}
}
