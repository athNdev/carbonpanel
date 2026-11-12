// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package proxy

import (
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/pkg/logger"
)

// TestGetOrCreateSessionRace hammers one session key concurrently. On the
// pre-fix code every racer that missed the read-lock lookup created its own
// backend socket and the losers' sockets (plus their response goroutines)
// leaked; the fixed code re-checks under the write lock so exactly one
// socket survives and every loser socket is closed.
func TestGetOrCreateSessionRace(t *testing.T) {
	p := NewUDPProxy(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New()})
	var created atomic.Int32
	var mu sync.Mutex
	var conns []*net.UDPConn
	p.listenBackend = func(network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
		c, err := net.ListenUDP(network, &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
		if err != nil {
			return nil, err
		}
		created.Add(1)
		mu.Lock()
		conns = append(conns, c)
		mu.Unlock()
		return c, nil
	}

	p.AddRoute("srv", "udp", "127.0.0.1", 19132)
	defer p.cancel()

	clientAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:50000")
	if err != nil {
		t.Fatal(err)
	}

	const workers = 32
	const iters = 25
	results := make([][]*udpSession, workers)
	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				s, err := p.getOrCreateSession(clientAddr)
				if err != nil {
					t.Errorf("getOrCreateSession: %v", err)
					return
				}
				results[w] = append(results[w], s)
			}
		}(w)
	}
	wg.Wait()

	// Exactly one session, and every caller got the same pointer.
	p.sessionsMu.RLock()
	sessionCount := len(p.sessions)
	stored := p.sessions[clientAddr.String()]
	p.sessionsMu.RUnlock()
	if sessionCount != 1 {
		t.Fatalf("sessions map has %d entries, want exactly 1", sessionCount)
	}
	for w := 0; w < workers; w++ {
		for i, s := range results[w] {
			if s == nil {
				t.Fatalf("worker %d iter %d got nil session", w, i)
			}
			if s != stored {
				t.Fatalf("worker %d iter %d got a losing session that should have been discarded", w, i)
			}
		}
	}

	// Every created socket except the winner's must already be closed by the
	// loser path: a second Close on a closed socket errors, while closing the
	// live winner succeeds.
	mu.Lock()
	defer mu.Unlock()
	if len(conns) == 0 {
		t.Fatal("no backend sockets were created at all")
	}
	t.Logf("backend sockets created for %d racing lookups: %d", workers*iters, len(conns))
	losers := 0
	for _, c := range conns {
		if c == stored.backendConn {
			continue
		}
		losers++
		if err := c.Close(); err == nil {
			t.Errorf("loser backend socket still open: race loser did not clean up")
		}
	}
	if losers != len(conns)-1 {
		t.Errorf("winner socket accounting wrong: %d conns, %d losers", len(conns), losers)
	}

	// The winner's socket must still be live until session removal.
	if err := stored.backendConn.Close(); err != nil {
		t.Errorf("winner backend socket should be open, Close failed: %v", err)
	}
	p.removeSession(clientAddr.String())
}

// TestGetOrCreateSessionNoBackend ensures the error path creates no socket.
func TestGetOrCreateSessionNoBackend(t *testing.T) {
	calls := 0
	p := NewUDPProxy(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New()})
	p.listenBackend = func(network string, laddr *net.UDPAddr) (*net.UDPConn, error) {
		calls++
		return net.ListenUDP(network, &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	}
	defer p.cancel()
	addr, _ := net.ResolveUDPAddr("udp", "127.0.0.1:50001")
	if _, err := p.getOrCreateSession(addr); err == nil {
		t.Fatal("expected no-backend error")
	}
	if calls != 0 {
		t.Errorf("created %d sockets despite missing backend", calls)
	}
}

func TestUDPProxyLoopbackRoundTrip(t *testing.T) {
	backend, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backend.Close() }()
	backendDone := make(chan error, 1)
	go func() {
		buf := make([]byte, 128)
		_ = backend.SetDeadline(time.Now().Add(3 * time.Second))
		n, addr, err := backend.ReadFromUDP(buf)
		if err == nil {
			_, err = backend.WriteToUDP(buf[:n], addr)
		}
		backendDone <- err
	}()

	p := NewUDPProxy(&Config{ListenAddr: "127.0.0.1:0", Logger: logger.New(), Listeners: loopbackTestListeners()})
	var backendSockets atomic.Int32
	p.listenBackend = func(network string, _ *net.UDPAddr) (*net.UDPConn, error) {
		backendSockets.Add(1)
		return net.ListenUDP(network, &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	}
	p.AddRoute("test", "udp", "127.0.0.1", backend.LocalAddr().(*net.UDPAddr).Port)
	if err := p.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop() }()
	client, err := net.DialUDP("udp", nil, p.conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	_ = client.SetDeadline(time.Now().Add(3 * time.Second))
	want := "loopback forwarding"
	if _, err := client.Write([]byte(want)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 128)
	n, err := client.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(buf[:n]); got != want {
		t.Fatalf("reply=%q, want %q", got, want)
	}
	if err := <-backendDone; err != nil {
		t.Fatal(err)
	}
	if got := backendSockets.Load(); got != 1 {
		t.Fatalf("created %d backend sockets, want one", got)
	}
}
