// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldview

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/auth"
	storage "github.com/athNdev/carbon-panel/internal/db"
)

// All fixtures in this package use local httptest servers and in-memory fakes.
// No live renderer, Docker container, Minecraft server or browser is involved;
// test names deliberately say "Fixture" so results are never read as live
// renderer evidence.

type fakeAuth struct {
	users map[string]*auth.AuthenticatedUser
}

func (f *fakeAuth) AuthenticateFromHeader(_ context.Context, header string) (*auth.AuthenticatedUser, error) {
	token := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	if u, ok := f.users[token]; ok {
		return u, nil
	}
	return nil, errors.New("fixture: invalid token")
}

type fakeEnforcer struct {
	mu      sync.Mutex
	allow   func(roles []string, resource, action, objectID string) (bool, error)
	calls   int
	lastObj string
}

func (f *fakeEnforcer) Enforce(roles []string, resource, action, objectID string) (bool, error) {
	f.mu.Lock()
	f.calls++
	f.lastObj = objectID
	fn := f.allow
	f.mu.Unlock()
	if fn == nil {
		return false, nil
	}
	return fn(roles, resource, action, objectID)
}

type fakeStore struct {
	mu       sync.Mutex
	servers  map[string]*storage.Server
	configs  map[string]*storage.ServerConfig
	subusers map[string][]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		servers: make(map[string]*storage.Server),
		configs: make(map[string]*storage.ServerConfig),
		subusers: make(map[string][]string),
	}
}

func (f *fakeStore) addServer(id, nodeID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.servers[id] = &storage.Server{ID: id, Name: "fixture-" + id, NodeID: nodeID}
}

func (f *fakeStore) setLevelName(serverID, level string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.configs[serverID] = &storage.ServerConfig{
		ServerID: serverID, CustomServerProperties: strPtr("level-name=" + level + "\n"),
	}
}

func (f *fakeStore) grant(userID, serverID, permission string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := serverID + "|" + userID
	f.subusers[key] = append(f.subusers[key], permission)
}

func strPtr(s string) *string { return &s }

func (f *fakeStore) ListServers(context.Context) ([]*storage.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]*storage.Server, 0, len(f.servers))
	for _, s := range f.servers {
		out = append(out, s)
	}
	return out, nil
}

func (f *fakeStore) GetServer(_ context.Context, id string) (*storage.Server, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.servers[id]; ok {
		return s, nil
	}
	return nil, errors.New("fixture: server not found")
}

func (f *fakeStore) GetServerConfig(_ context.Context, id string) (*storage.ServerConfig, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if cfg, ok := f.configs[id]; ok {
		return cfg, nil
	}
	return nil, storage.ErrServerConfigNotFound
}

func (f *fakeStore) SubuserPermissions(_ context.Context, serverID, userID string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.subusers[serverID+"|"+userID]...)
}

type fakeLocator struct {
	local map[string]bool
	err   error
}

func (f *fakeLocator) IsLocal(_ context.Context, nodeID string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.local[nodeID], nil
}

type fakeAuditor struct {
	mu      sync.Mutex
	entries []activity.Entry
	err     error
}

func (f *fakeAuditor) Log(e activity.Entry) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.entries = append(f.entries, e)
	return nil
}

func (f *fakeAuditor) all() []activity.Entry {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]activity.Entry(nil), f.entries...)
}

type countingDoer struct {
	mu    sync.Mutex
	inner HTTPDoer
	count int
}

func newCountingDoer(inner HTTPDoer) *countingDoer { return &countingDoer{inner: inner} }

func (c *countingDoer) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	if c.inner == nil {
		return http.DefaultClient.Do(req)
	}
	return c.inner.Do(req)
}

type funcDoer struct {
	mu    sync.Mutex
	count int
	fn    func(*http.Request) (*http.Response, error)
}

func (f *funcDoer) Do(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	f.count++
	f.mu.Unlock()
	return f.fn(req)
}

func (f *funcDoer) hits() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.count
}

func (c *countingDoer) hits() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

// endpointForFixture converts an httptest.Server URL into a loopback endpoint
// suitable for a local-node renderer fixture.
func endpointForFixture(t interface{ Helper() }, rawURL, path string, kind RendererKind) Endpoint {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		panic(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		panic(err)
	}
	return Endpoint{
		NodeID: "default", Kind: kind, Host: "127.0.0.1", Port: port,
		BasePath: path,
	}
}
