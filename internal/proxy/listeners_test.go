// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package proxy

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/pkg/logger"
)

func loopbackTestListeners() ListenerFactory {
	return ListenerFactory{
		TCP: loopbackOnlyDirectKnockListener,
		UDP: func(network string, addr *net.UDPAddr) (*net.UDPConn, error) {
			if addr == nil || !addr.IP.IsLoopback() {
				return nil, fmt.Errorf("test refuses non-loopback UDP bind %v", addr)
			}
			return net.ListenUDP(network, addr)
		},
	}
}

func freeUDPPort(t *testing.T) int {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	port := conn.LocalAddr().(*net.UDPAddr).Port
	if err := conn.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

// Every manager constructor path reaches the rejecting adapter before the OS.
// A forgotten BindHost propagation fails instead of opening a public socket.
func TestManagerListenerPolicy(t *testing.T) {
	for _, host := range []string{"", "127.0.0.1"} {
		for _, path := range []string{"start", "add", "tcp", "minecraft", "http", "udp", "knock"} {
			t.Run(host+"/"+path, func(t *testing.T) {
				store := setupTestStore(t)
				defer store.Close()
				cfg := &config.Config{Proxy: config.ProxyConfig{Enabled: true, BindHost: host}}
				factory := loopbackTestListeners()
				calls := 0
				seenHost := "unset"
				factory.TCP = func(network, address string) (net.Listener, error) {
					calls++
					var err error
					seenHost, _, err = net.SplitHostPort(address)
					if err != nil {
						return nil, err
					}
					return loopbackOnlyDirectKnockListener(network, address)
				}
				udp := factory.UDP
				factory.UDP = func(network string, address *net.UDPAddr) (*net.UDPConn, error) {
					calls++
					seenHost = ""
					if address != nil && address.IP != nil {
						seenHost = address.IP.String()
					}
					return udp(network, address)
				}
				mgr := NewManagerWithListeners(store, cfg, logger.New(), factory, &mockDockerClient{})
				defer mgr.Stop()
				var err error
				switch path {
				case "start":
					if err := store.CreateProxyListener(context.Background(), &db.ProxyListener{ID: "test", Port: 0, Enabled: true}); err != nil {
						t.Fatal(err)
					}
					err = mgr.Start()
				case "add":
					err = mgr.AddListener(&db.ProxyListener{ID: "test", Port: 0, Enabled: true})
				case "knock":
					err = mgr.directKnocks.StartKnock(&db.Server{ID: "test", Port: getFreePort(t)})
				default:
					mgr.mu.Lock()
					err = mgr.addPortRouteUnlocked("test", "example.test", "127.0.0.1", 0, 25565, path, "test", "test")
					mgr.mu.Unlock()
				}
				if calls != 1 || seenHost != host {
					t.Fatalf("listener calls=%d host=%q, want one call host=%q", calls, seenHost, host)
				}
				if host == "" && err == nil {
					t.Fatal("public production default escaped the rejecting test adapter")
				}
				if host != "" && err != nil {
					t.Fatalf("loopback listener failed: %v", err)
				}
			})
		}
	}
}

func TestManagerBindHostIPv6(t *testing.T) {
	mgr := &Manager{config: &config.ProxyConfig{BindHost: "::1"}}
	if got := mgr.listenAddress(25565); got != "[::1]:25565" {
		t.Fatalf("IPv6 listen address = %q", got)
	}
}
