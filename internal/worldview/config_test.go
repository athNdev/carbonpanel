// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"errors"
	"testing"
)

// TestEmptyConfigIsAbsentFixture pins the default: with no explicit renderer
// topology every world is absent rather than probing a guessed local port.
func TestEmptyConfigIsAbsentFixture(t *testing.T) {
	r := NewResolver(Config{}, &fakeLocator{local: map[string]bool{"default": true}})
	if _, err := r.Resolve(context.Background(), "default", "srv-a"); !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("empty config Resolve error = %v, want ErrRendererUnavailable", err)
	}
	var nilResolver *Resolver
	if _, err := nilResolver.Resolve(context.Background(), "default", "srv-a"); !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("nil resolver error = %v, want ErrRendererUnavailable", err)
	}
}

// TestLocalEndpointMustBeLoopbackFixture rejects a local-node renderer bound
// to a LAN address: renderer containers must not publish a public port.
func TestLocalEndpointMustBeLoopbackFixture(t *testing.T) {
	loc := &fakeLocator{local: map[string]bool{"default": true}}
	lan := Config{Endpoints: []Endpoint{{NodeID: "default", Kind: RendererBlueMap, Host: "192.168.1.20", Port: 8080}}}
	if _, err := NewResolver(lan, loc).Resolve(context.Background(), "default", "srv-a"); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("LAN host for local node error = %v, want ErrInvalidEndpoint", err)
	}
	loop := Config{Endpoints: []Endpoint{{NodeID: "default", Kind: RendererBlueMap, Host: "127.0.0.1", Port: 8080}}}
	if _, err := NewResolver(loop, loc).Resolve(context.Background(), "default", "srv-a"); err != nil {
		t.Fatalf("loopback host for local node failed: %v", err)
	}
	v6 := Config{Endpoints: []Endpoint{{NodeID: "default", Kind: RendererBlueMap, Host: "::1", Port: 8080}}}
	if _, err := NewResolver(v6, loc).Resolve(context.Background(), "default", "srv-a"); err != nil {
		t.Fatalf("IPv6 loopback host for local node failed: %v", err)
	}
}

// TestRemoteEndpointRequiresAllowlistedNodeLocalHostFixture ensures a remote
// node never falls back to the controller's localhost and never uses an
// unapproved host.
func TestRemoteEndpointRequiresAllowlistedNodeLocalHostFixture(t *testing.T) {
	loc := &fakeLocator{local: map[string]bool{"default": true, "remote-1": false}}
	remote := Endpoint{NodeID: "remote-1", Kind: RendererBlueMap, Host: "10.0.0.9", Port: 8080}
	if _, err := NewResolver(Config{Endpoints: []Endpoint{remote}}, loc).Resolve(context.Background(), "remote-1", "srv-r"); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("unallowlisted remote host error = %v, want ErrInvalidEndpoint", err)
	}
	cfg := Config{
		Endpoints: []Endpoint{remote},
		AllowedHosts: []string{"10.0.0.9"},
	}
	if _, err := NewResolver(cfg, loc).Resolve(context.Background(), "remote-1", "srv-r"); err != nil {
		t.Fatalf("allowlisted remote node-local host failed: %v", err)
	}
	cfg.Endpoints[0].Host = "127.0.0.1"
	if _, err := NewResolver(cfg, loc).Resolve(context.Background(), "remote-1", "srv-r"); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("remote node using controller loopback error = %v, want ErrInvalidEndpoint", err)
	}
}

// TestNodeClassificationFailureIsUnavailableFixture fails closed when the
// node's locality cannot be determined.
func TestNodeClassificationFailureIsUnavailableFixture(t *testing.T) {
	loc := &fakeLocator{err: errors.New("node store unavailable")}
	cfg := Config{Endpoints: []Endpoint{{NodeID: "node-x", Kind: RendererBlueMap, Host: "10.0.0.9", Port: 8080}}}
	if _, err := NewResolver(cfg, loc).Resolve(context.Background(), "node-x", "srv-x"); !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("node classification failure error = %v, want ErrRendererUnavailable", err)
	}
}

// TestPerServerEndpointOverrideFixture confirms the more specific server
// override wins over the node-wide renderer location.
func TestPerServerEndpointOverrideFixture(t *testing.T) {
	loc := &fakeLocator{local: map[string]bool{"default": true}}
	cfg := Config{Endpoints: []Endpoint{
		{NodeID: "default", Kind: RendererSquaremap, Host: "127.0.0.1", Port: 1},
		{NodeID: "default", ServerID: "srv-b", Kind: RendererBlueMap, Host: "::1", Port: 2},
	}}
	r := NewResolver(cfg, loc)
	first, err := r.Resolve(context.Background(), "default", "srv-a")
	if err != nil || first.Type() != rendererType(RendererSquaremap) {
		t.Fatalf("node-wide renderer = %v, %v; want squaremap", first, err)
	}
	second, err := r.Resolve(context.Background(), "default", "srv-b")
	if err != nil || second.Type() != rendererType(RendererBlueMap) {
		t.Fatalf("per-server renderer = %v, %v; want bluemap", second, err)
	}
}
