// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Executable conformance evidence for the OSS-374 stream family. This file is
// additive and non-overlapping: it registers a TEST-LOCAL MCP tool surface over
// the exported FamilyStreamClients seam, then drives it with a real MCP SDK
// client over the stdio wire. Every tool call reaches a real generated Connect
// client, the production auth interceptor and a procedure-specific handler
// that records the typed request and returns a distinguishable outcome.
//
// It is not the production catalog (owned by the integration owner). It proves
// that the typed paths named by the audit are callable, bounded, scoped and
// fail-closed, and that the byte channels carry intact bytes only inside
// bounded sessions. Raw byte routes, OIDC, /ws, health and the map proxy stay
// transport/private mechanics and are never registered as tools here.
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	pkgdownload "github.com/athNdev/carbon-panel/pkg/download"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	pkgupload "github.com/athNdev/carbon-panel/pkg/upload"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// confRecorder observes the real HTTP status of every Connect call without
// replacing the transport or inventing a response.
type confRecorder struct {
	base     connect.HTTPClient
	mu       sync.Mutex
	statuses map[string][]int
}

func (r *confRecorder) Do(req *http.Request) (*http.Response, error) {
	resp, err := r.base.Do(req)
	if resp != nil {
		r.mu.Lock()
		r.statuses[req.URL.Path] = append(r.statuses[req.URL.Path], resp.StatusCode)
		r.mu.Unlock()
	}
	return resp, err
}

func (r *confRecorder) status(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	list := r.statuses[path]
	if len(list) == 0 {
		return 0
	}
	return list[len(list)-1]
}

type confFixture struct {
	store    *storage.Store
	enforcer *rbac.Enforcer
	auth     *auth.Manager
	tokens   map[string]string
	server   *httptest.Server
	rec      *confRecorder

	mu     sync.Mutex
	visits map[string][]string
}

func (f *confFixture) record(procedure, request string) {
	f.mu.Lock()
	f.visits[procedure] = append(f.visits[procedure], request)
	f.mu.Unlock()
}

func (f *confFixture) visitCount(procedure string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.visits[procedure])
}

func (f *confFixture) lastVisit(procedure string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.visits[procedure]
	if len(list) == 0 {
		return ""
	}
	return list[len(list)-1]
}

func confRegister[Req, Res any](f *confFixture, mux *http.ServeMux, procedure string,
	ic connect.UnaryInterceptorFunc, result func(*Req) *Res) {
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(_ context.Context, req *connect.Request[Req]) (*connect.Response[Res], error) {
			f.record(procedure, fmt.Sprintf("%v", req.Msg))
			return connect.NewResponse(result(req.Msg)), nil
		}, connect.WithInterceptors(ic)))
}

// confRegisterTypedHandlers mounts one DISTINCT handler and response per
// typed path. The returned values are conformance markers, not zero-value
// mocks, so a silent mis-route cannot pass as success.
func confRegisterTypedHandlers(f *confFixture, mux *http.ServeMux, ic connect.UnaryInterceptorFunc) {
	confRegister(f, mux, "/carbonpanel.v1.ConfigService/ValidateProviderKey", ic,
		func(*v1.ValidateProviderKeyRequest) *v1.ValidateProviderKeyResponse {
			return &v1.ValidateProviderKeyResponse{Valid: true, Message: "conf-valid"}
		})
	confRegister(f, mux, "/carbonpanel.v1.NodeService/PingNode", ic,
		func(*v1.PingNodeRequest) *v1.PingNodeResponse {
			return &v1.PingNodeResponse{Success: true, Message: "conf-pong", LatencyMs: 7}
		})
	confRegister(f, mux, "/carbonpanel.v1.ModBrowseService/SearchMods", ic,
		func(*v1.SearchModsRequest) *v1.SearchModsResponse {
			return &v1.SearchModsResponse{Count: 1}
		})
	confRegister(f, mux, "/carbonpanel.v1.ModBrowseService/GetModVersions", ic,
		func(*v1.GetModVersionsRequest) *v1.GetModVersionsResponse {
			return &v1.GetModVersionsResponse{Count: 2}
		})
	confRegister(f, mux, "/carbonpanel.v1.ModBrowseService/InstallMods", ic,
		func(*v1.InstallModsRequest) *v1.InstallModsResponse {
			return &v1.InstallModsResponse{Success: true, Installed: []string{"conf.jar"}}
		})
	confRegister(f, mux, "/carbonpanel.v1.MinecraftService/GetLoaderVersions", ic,
		func(*v1.GetLoaderVersionsRequest) *v1.GetLoaderVersionsResponse {
			return &v1.GetLoaderVersionsResponse{Versions: []string{"conf-loader"}}
		})
	confRegister(f, mux, "/carbonpanel.v1.ModpackService/GetModpackConfig", ic,
		func(*v1.GetModpackConfigRequest) *v1.GetModpackConfigResponse {
			return &v1.GetModpackConfigResponse{Config: map[string]string{"conf": "ok"}}
		})
	confRegister(f, mux, "/carbonpanel.v1.ConfigService/GetUpdateBatch", ic,
		func(*v1.GetUpdateBatchRequest) *v1.GetUpdateBatchResponse {
			return &v1.GetUpdateBatchResponse{Open: true, IdleMinutes: 3}
		})
	confRegister(f, mux, "/carbonpanel.v1.ConfigService/UpdateServerConfig", ic,
		func(*v1.UpdateServerConfigRequest) *v1.UpdateServerConfigResponse {
			return &v1.UpdateServerConfigResponse{Staged: true}
		})
	confRegister(f, mux, "/carbonpanel.v1.WorldViewService/GetWorldViewStatus", ic,
		func(*v1.GetWorldViewStatusRequest) *v1.GetWorldViewStatusResponse {
			return &v1.GetWorldViewStatusResponse{Target: &v1.WorldViewTarget{ServerId: "srv-1", Name: "conf-world"}}
		})
}

func newConfFixture(t *testing.T) *confFixture {
	t.Helper()
	ctx := context.Background()
	store := newTestStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatalf("SeedDefaultPolicies: %v", err)
	}
	authManager, err := auth.NewManager(store, enforcer, &config.AuthConfig{
		Local:           config.LocalConfig{Enabled: true},
		OIDC:            config.OIDCConfig{Enabled: false},
		AnonymousAccess: false,
		AllowNoAuth:     false,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	interceptor := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())

	if err := store.CreateServer(ctx, &storage.Server{ID: "srv-1", Name: "fixture", DataPath: "fixture"}); err != nil {
		t.Fatalf("CreateServer: %v", err)
	}

	rec := &confRecorder{base: http.DefaultClient, statuses: map[string][]int{}}
	f := &confFixture{store: store, enforcer: enforcer, auth: authManager, rec: rec, visits: map[string][]string{}}
	mux := http.NewServeMux()
	registerAuthHandlers(mux, interceptor, true)
	confRegisterTypedHandlers(f, mux, interceptor)

	f.tokens = map[string]string{
		"admin":      seedToken(t, store, enforcer, "u-conf-admin", "admin", nil),
		"servers":    seedToken(t, store, enforcer, "u-conf-servers", "conf-servers", globalPerms("servers", "read")),
		"modsRead":   seedToken(t, store, enforcer, "u-conf-mods", "conf-mods-read", globalPerms("mods", "read")),
		"configRead": seedToken(t, store, enforcer, "u-conf-config", "conf-config-read", globalPerms("server_config", "read")),
		"invalid":    "dp_nonexistent_token",
	}
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func (f *confFixture) clients(t *testing.T, role string) *FamilyStreamClients {
	t.Helper()
	clients, err := NewFamilyStreamClients(f.rec, f.server.URL, f.tokens[role])
	if err != nil {
		t.Fatalf("NewFamilyStreamClients(%s): %v", role, err)
	}
	return clients
}

// confArgs is the bounded envelope for the test-local conformance tools.
type confArgs struct {
	Provider  string `json:"provider,omitempty"`
	APIKey    string `json:"api_key,omitempty"`
	ID        string `json:"id,omitempty"`
	ServerID  string `json:"server_id,omitempty"`
	Query     string `json:"query,omitempty"`
	Project   string `json:"project,omitempty"`
	Loader    string `json:"loader,omitempty"`
	MCVersion string `json:"mc_version,omitempty"`
	Confirm   bool   `json:"confirm,omitempty"`
	Limit     int32  `json:"limit,omitempty"`
}

func (a confArgs) Confirmation() bool { return a.Confirm }

func confInvalid(tool, procedure, field, rule string) error {
	return &ToolError{
		Kind:      ErrKindInvalidArguments,
		Tool:      tool,
		Procedure: procedure,
		Message:   fmt.Sprintf("%s for tool %q: %s", field, tool, rule),
	}
}

func confAddTool[In any](s *sdkmcp.Server, name, procedure string, confirm bool,
	clients *FamilyStreamClients, validate func(In) error,
	run func(context.Context, *FamilyStreamClients, In) (any, error)) {
	spec, ok := FamilyStreamToolSpec(procedure)
	if !ok {
		panic("conformance tool references an undeclared procedure: " + procedure)
	}
	spec.Name = name
	spec.Confirm = confirm
	spec.Description = "Conformance path for " + procedure + ". Bounded and ACL-scoped."
	sdkmcp.AddTool(s, &sdkmcp.Tool{Name: name, Description: spec.Description},
		func(ctx context.Context, _ *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, ToolOutput, error) {
			if confirm {
				c, ok := any(in).(interface{ Confirmation() bool })
				if !ok || !c.Confirmation() {
					return failure(spec, &ToolError{
						Kind:      ErrKindConfirmationRequired,
						Tool:      name,
						Procedure: procedure,
						Message:   name + " is a mutation: set confirm=true to proceed",
					})
				}
			}
			if err := validate(in); err != nil {
				// Shared arg helpers key ToolError.Procedure by catalog tool name;
				// these conformance names are test-local, so complete the tuple.
				var te *ToolError
				if errors.As(err, &te) {
					fixed := *te
					if fixed.Tool == "" {
						fixed.Tool = name
					}
					if fixed.Procedure == "" {
						fixed.Procedure = spec.Procedure
					}
					return failure(spec, &fixed)
				}
				return failure(spec, err)
			}
			result, err := run(ctx, clients, in)
			if err != nil {
				return failure(spec, err)
			}
			return nil, ToolOutput{Status: "ok", Result: result}, nil
		})
}

type confToolCase struct {
	tool        string
	procedure   string
	marker      string
	request     string // substring expected in the recorded typed request
	valid       map[string]any
	invalid     map[string]any
	invalidKind string
	denyRole    string
	confirm     bool
}

func confToolCases() []confToolCase {
	return []confToolCase{
		{"stream_validate_provider_key", "/carbonpanel.v1.ConfigService/ValidateProviderKey", `"valid":true`, `provider:"curseforge"`,
			map[string]any{"provider": "curseforge", "api_key": "fixture"}, map[string]any{"provider": "not-a-provider"}, ErrKindInvalidArguments, "servers", false},
		{"stream_ping_node", "/carbonpanel.v1.NodeService/PingNode", `"latency_ms":7`, `id:"node-1"`,
			map[string]any{"id": "node-1"}, map[string]any{"id": strings.Repeat("x", maxIDLen+1)}, ErrKindInvalidArguments, "servers", false},
		{"stream_search_mods", "/carbonpanel.v1.ModBrowseService/SearchMods", `"count":1`, `server_id:"srv-1"`,
			map[string]any{"server_id": "srv-1", "query": "map", "limit": 10}, map[string]any{"server_id": "srv-1", "limit": maxLimit + 1}, ErrKindInvalidArguments, "servers", false},
		{"stream_get_mod_versions", "/carbonpanel.v1.ModBrowseService/GetModVersions", `"count":2`, `server_id:"srv-1"`,
			map[string]any{"server_id": "srv-1", "project": "map"}, map[string]any{"project": "map"}, ErrKindInvalidArguments, "servers", false},
		{"stream_install_mods", "/carbonpanel.v1.ModBrowseService/InstallMods", `"installed":["conf.jar"]`, `server_id:"srv-1"`,
			map[string]any{"server_id": "srv-1", "confirm": true}, map[string]any{"server_id": "srv-1"}, ErrKindConfirmationRequired, "modsRead", true},
		{"stream_get_loader_versions", "/carbonpanel.v1.MinecraftService/GetLoaderVersions", `"versions":["conf-loader"]`, `mc_version:"1.21"`,
			map[string]any{"loader": "fabric", "mc_version": "1.21"}, map[string]any{"loader": strings.Repeat("x", 33), "mc_version": "1.21"}, ErrKindInvalidArguments, "invalid", false},
		{"stream_get_modpack_config", "/carbonpanel.v1.ModpackService/GetModpackConfig", `"conf":"ok"`, `id:"pack-1"`,
			map[string]any{"id": "pack-1"}, map[string]any{}, ErrKindInvalidArguments, "servers", false},
		{"stream_get_update_batch", "/carbonpanel.v1.ConfigService/GetUpdateBatch", `"open":true`, `server_id:"srv-1"`,
			map[string]any{"server_id": "srv-1"}, map[string]any{"server_id": strings.Repeat("x", maxIDLen+1)}, ErrKindInvalidArguments, "servers", false},
		{"stream_update_server_config", "/carbonpanel.v1.ConfigService/UpdateServerConfig", `"staged":true`, `stage:true`,
			map[string]any{"server_id": "srv-1", "confirm": true}, map[string]any{"server_id": "srv-1"}, ErrKindConfirmationRequired, "configRead", true},
		{"stream_get_world_view_status", "/carbonpanel.v1.WorldViewService/GetWorldViewStatus", `"server_id":"srv-1"`, `server_id:"srv-1"`,
			map[string]any{"server_id": "srv-1"}, map[string]any{}, ErrKindInvalidArguments, "servers", false},
	}
}

func confValidate(tool, procedure string, in confArgs) error {
	switch procedure {
	case "/carbonpanel.v1.ConfigService/ValidateProviderKey":
		if !strings.EqualFold(in.Provider, "curseforge") && !strings.EqualFold(in.Provider, "modrinth") {
			return confInvalid(tool, procedure, "provider", "must be curseforge or modrinth")
		}
		if err := requireText(tool, "api_key", in.APIKey, 512); err != nil {
			return err
		}
	case "/carbonpanel.v1.NodeService/PingNode", "/carbonpanel.v1.ModpackService/GetModpackConfig":
		if err := requireID(tool, "id", in.ID); err != nil {
			return err
		}
	case "/carbonpanel.v1.ModBrowseService/SearchMods":
		if err := requireID(tool, "server_id", in.ServerID); err != nil {
			return err
		}
		if err := requireText(tool, "query", in.Query, maxQueryLen); err != nil {
			return err
		}
		if _, err := limitOr(tool, in.Limit); err != nil {
			return err
		}
	case "/carbonpanel.v1.ModBrowseService/GetModVersions", "/carbonpanel.v1.ModBrowseService/InstallMods",
		"/carbonpanel.v1.ConfigService/GetUpdateBatch", "/carbonpanel.v1.ConfigService/UpdateServerConfig",
		"/carbonpanel.v1.WorldViewService/GetWorldViewStatus":
		if err := requireID(tool, "server_id", in.ServerID); err != nil {
			return err
		}
	case "/carbonpanel.v1.MinecraftService/GetLoaderVersions":
		if err := requireText(tool, "loader", in.Loader, 32); err != nil {
			return err
		}
		if err := requireText(tool, "mc_version", in.MCVersion, 32); err != nil {
			return err
		}
		if in.Loader == "" || in.MCVersion == "" {
			return confInvalid(tool, procedure, "loader/mc_version", "both are required")
		}
	}
	return nil
}

func confRegisterConformanceTools(s *sdkmcp.Server, clients *FamilyStreamClients) {
	for _, tc := range confToolCases() {
		tc := tc
		confAddTool(s, tc.tool, tc.procedure, tc.confirm, clients,
			func(in confArgs) error { return confValidate(tc.tool, tc.procedure, in) },
			func(ctx context.Context, c *FamilyStreamClients, in confArgs) (any, error) {
				switch tc.procedure {
				case "/carbonpanel.v1.ConfigService/ValidateProviderKey":
					return invoke(ctx, c.Config.ValidateProviderKey, v1.ValidateProviderKeyRequest{Provider: in.Provider, ApiKey: in.APIKey})
				case "/carbonpanel.v1.NodeService/PingNode":
					return invoke(ctx, c.Node.PingNode, v1.PingNodeRequest{Id: in.ID})
				case "/carbonpanel.v1.ModBrowseService/SearchMods":
					// The bound is enforced in Go; SearchModsRequest carries no
					// limit field on this build.
					if _, err := limitOr(tc.tool, in.Limit); err != nil {
						return nil, err
					}
					return invoke(ctx, c.ModBrowse.SearchMods, v1.SearchModsRequest{ServerId: in.ServerID, Query: in.Query})
				case "/carbonpanel.v1.ModBrowseService/GetModVersions":
					return invoke(ctx, c.ModBrowse.GetModVersions, v1.GetModVersionsRequest{ServerId: in.ServerID, Project: in.Project})
				case "/carbonpanel.v1.ModBrowseService/InstallMods":
					return invoke(ctx, c.ModBrowse.InstallMods, v1.InstallModsRequest{ServerId: in.ServerID})
				case "/carbonpanel.v1.MinecraftService/GetLoaderVersions":
					return invoke(ctx, c.Minecraft.GetLoaderVersions, v1.GetLoaderVersionsRequest{Loader: in.Loader, McVersion: in.MCVersion})
				case "/carbonpanel.v1.ModpackService/GetModpackConfig":
					return invoke(ctx, c.Modpack.GetModpackConfig, v1.GetModpackConfigRequest{Id: in.ID})
				case "/carbonpanel.v1.ConfigService/GetUpdateBatch":
					return invoke(ctx, c.Config.GetUpdateBatch, v1.GetUpdateBatchRequest{ServerId: in.ServerID})
				case "/carbonpanel.v1.ConfigService/UpdateServerConfig":
					return invoke(ctx, c.Config.UpdateServerConfig, v1.UpdateServerConfigRequest{ServerId: in.ServerID, Stage: true})
				case "/carbonpanel.v1.WorldViewService/GetWorldViewStatus":
					return invoke(ctx, c.WorldView.GetWorldViewStatus, v1.GetWorldViewStatusRequest{ServerId: in.ServerID})
				}
				return nil, confInvalid(tc.tool, tc.procedure, "procedure", "unmapped conformance path")
			})
	}
}

func confResultText(t *testing.T, out ToolOutput) string {
	t.Helper()
	raw, err := json.Marshal(out.Result)
	if err != nil {
		t.Fatalf("marshal result: %v", err)
	}
	return string(raw)
}

func TestFamilyStreamConformanceTypedTools(t *testing.T) {
	f := newConfFixture(t)
	sessions := map[string]*sdkmcp.ClientSession{}
	for role, token := range f.tokens {
		baseClients, err := NewClients(f.rec, f.server.URL, token)
		if err != nil {
			t.Fatalf("NewClients(%s): %v", role, err)
		}
		server := NewServer(baseClients)
		confRegisterConformanceTools(server, f.clients(t, role))
		sessions[role] = connectStdioSession(t, server, context.Background())
	}

	successes, denials, bounded, gates := 0, 0, 0, 0
	for _, tc := range confToolCases() {
		t.Run(tc.tool+"/success", func(t *testing.T) {
			before := f.visitCount(tc.procedure)
			out := callTool(t, sessions["admin"], tc.tool, tc.valid)
			assertSuccess(t, out, tc.tool)
			text := confResultText(t, out)
			if !strings.Contains(text, tc.marker) {
				t.Fatalf("%s: outcome marker %q missing from %s", tc.tool, tc.marker, text)
			}
			if got := f.visitCount(tc.procedure); got != before+1 {
				t.Fatalf("%s: typed handler reached %d times, want %d", tc.tool, got-before, 1)
			}
			if !strings.Contains(f.lastVisit(tc.procedure), tc.request) {
				t.Fatalf("%s: bounded request %q missing from %q", tc.tool, tc.request, f.lastVisit(tc.procedure))
			}
			if got := f.rec.status(tc.procedure); got != http.StatusOK {
				t.Fatalf("%s: real handler status %d, want 200", tc.tool, got)
			}
		})

		t.Run(tc.tool+"/scoped_denial", func(t *testing.T) {
			before := f.visitCount(tc.procedure)
			out := callTool(t, sessions[tc.denyRole], tc.tool, tc.valid)
			wantKind := ErrKindPermissionDenied
			want := http.StatusForbidden
			if tc.denyRole == "invalid" {
				wantKind = ErrKindUnauthenticated
				want = http.StatusUnauthorized
			}
			e := assertStructuredError(t, out, tc.tool, wantKind)
			if e.Tool != tc.tool || e.Procedure != tc.procedure || e.Code == "" {
				t.Fatalf("%s: incomplete structured denial: %+v", tc.tool, e)
			}
			if got := f.visitCount(tc.procedure); got != before {
				t.Fatalf("%s: denied caller reached the typed handler", tc.tool)
			}
			if got := f.rec.status(tc.procedure); got != want {
				t.Fatalf("%s: real handler status %d, want %d", tc.tool, got, want)
			}
		})

		t.Run(tc.tool+"/bounded_arguments", func(t *testing.T) {
			before := f.visitCount(tc.procedure)
			out := callTool(t, sessions["admin"], tc.tool, tc.invalid)
			e := assertStructuredError(t, out, tc.tool, tc.invalidKind)
			if e.Tool != tc.tool || e.Procedure != tc.procedure {
				t.Fatalf("%s: incomplete argument error: %+v", tc.tool, e)
			}
			if got := f.visitCount(tc.procedure); got != before {
				t.Fatalf("%s: invalid arguments reached the typed handler", tc.tool)
			}
			if tc.invalidKind == ErrKindConfirmationRequired {
				gates++
			} else {
				bounded++
			}
		})
		successes++
		denials++
	}
	t.Logf("OSS-374 conformance: typed tools=%d success=%d scoped_denial=%d bounded=%d confirmation_gate=%d",
		len(confToolCases()), successes, denials, bounded, gates)
}

func TestFamilyStreamConformanceByteSessions(t *testing.T) {
	f := newConfFixture(t)
	log := logger.New()
	payload := make([]byte, 64*1024)
	for i := range payload {
		payload[i] = byte(i*31 + 7)
	}
	sum := sha256.Sum256(payload)
	wantHash := hex.EncodeToString(sum[:])

	// --- upload: real bytes, real ACLs, real size cap --------------------
	uploadMgr := pkgupload.NewManager(t.TempDir(), time.Hour, 1<<20, log)
	t.Cleanup(uploadMgr.Stop)
	session, err := uploadMgr.InitSession("conf.bin", int64(len(payload)), int32(len(payload)))
	if err != nil {
		t.Fatalf("InitSession: %v", err)
	}
	uploadOK := handlers.NewUploadStreamHandler(uploadMgr, f.auth, f.enforcer, log)
	uploadNoEnforcer := handlers.NewUploadStreamHandler(uploadMgr, f.auth, nil, log)
	uploadPath := "/api/v1/upload/" + session.ID

	rec := streamRequest(t, uploadOK, http.MethodPut, uploadPath, f.tokens["admin"], payload, http.StatusOK)
	var ack struct {
		Completed     bool  `json:"completed"`
		BytesReceived int64 `json:"bytes_received"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil || !ack.Completed || ack.BytesReceived != int64(len(payload)) {
		t.Fatalf("upload ack mismatched: %s (%v)", rec.Body.String(), err)
	}
	tempPath, _, err := uploadMgr.GetTempPath(session.ID)
	if err != nil {
		t.Fatalf("GetTempPath: %v", err)
	}
	stored, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatalf("ReadFile(temp): %v", err)
	}
	if got := sha256.Sum256(stored); hex.EncodeToString(got[:]) != wantHash {
		t.Fatal("uploaded bytes arrived corrupted")
	}

	// The same URL with an unrelated grant, no credential, or no enforcer
	// must never write a byte.
	before := len(stored)
	streamRequest(t, uploadOK, http.MethodPut, uploadPath, f.tokens["servers"], payload, http.StatusForbidden)
	streamRequest(t, uploadOK, http.MethodPut, uploadPath, "", payload, http.StatusUnauthorized)
	streamRequest(t, uploadNoEnforcer, http.MethodPut, uploadPath, f.tokens["admin"], payload, http.StatusForbidden)
	after, err := os.ReadFile(tempPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != before || sha256.Sum256(after) != sha256.Sum256(stored) {
		t.Fatal("denied upload changed the bounded session file")
	}

	// Oversize body must be refused without exceeding the session cap.
	capped, err := uploadMgr.InitSession("capped.bin", 1024, 1024)
	if err != nil {
		t.Fatal(err)
	}
	over := streamRequest(t, uploadOK, http.MethodPut, "/api/v1/upload/"+capped.ID, f.tokens["admin"], bytes.Repeat([]byte{0x41}, 1025), http.StatusInternalServerError)
	var overAck struct {
		BytesReceived int64 `json:"bytes_received"`
		Completed     bool  `json:"completed"`
	}
	if err := json.Unmarshal(over.Body.Bytes(), &overAck); err != nil || overAck.Completed || overAck.BytesReceived != 0 {
		t.Fatalf("oversize was not reported as incomplete: %s (%v)", over.Body.String(), err)
	}
	// GetTempPath intentionally refuses incomplete sessions; the session seam
	// still exposes the bounded staging path for cap verification.
	received, total, _, _, completed, _, err := uploadMgr.GetSessionStatus(capped.ID)
	if err != nil || completed || total != 1024 || received != 0 {
		t.Fatalf("capped session status: received=%d total=%d completed=%v err=%v", received, total, completed, err)
	}
	cappedSession, err := uploadMgr.GetSession(capped.ID)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(cappedSession.TempPath); err != nil || info.Size() > 1024 {
		t.Fatalf("upload cap not held: size=%v err=%v", info, err)
	}
	// Negative resume offset and a path-shaped session id are refused early.
	neg := httptest.NewRequest(http.MethodPut, uploadPath, bytes.NewReader(payload))
	neg.Header.Set("Authorization", "Bearer "+f.tokens["admin"])
	neg.Header.Set("X-Upload-Offset", "-1")
	negRec := httptest.NewRecorder()
	uploadOK.ServeHTTP(negRec, neg)
	if negRec.Code != http.StatusBadRequest {
		t.Fatalf("negative offset status %d", negRec.Code)
	}
	traversal := httptest.NewRequest(http.MethodPut, "/api/v1/upload/a/b", bytes.NewReader(payload))
	traversal.Header.Set("Authorization", "Bearer "+f.tokens["admin"])
	travRec := httptest.NewRecorder()
	uploadOK.ServeHTTP(travRec, traversal)
	if travRec.Code != http.StatusBadRequest {
		t.Fatalf("path-shaped session status %d", travRec.Code)
	}

	// --- download: exact bytes, range, scoped ACL, bounded session -------
	file := filepath.Join(t.TempDir(), "conf-world.zip")
	if err := os.WriteFile(file, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	dlMgr := pkgdownload.NewManager(t.TempDir(), time.Hour, log)
	t.Cleanup(dlMgr.Stop)
	dlSession := dlMgr.InitSessionFor(file, "conf-world.zip", int64(len(payload)), false, "srv-1", "u-conf-admin")
	dlOK := handlers.NewDownloadStreamHandler(dlMgr, f.auth, f.enforcer, log)
	dlNoEnforcer := handlers.NewDownloadStreamHandler(dlMgr, f.auth, nil, log)
	dlPath := "/api/v1/download/" + dlSession.ID

	full := streamRequest(t, dlOK, http.MethodGet, dlPath, f.tokens["admin"], nil, http.StatusOK)
	if !bytes.Equal(full.Body.Bytes(), payload) {
		t.Fatal("streamed download bytes were not intact")
	}
	rangeReq := httptest.NewRequest(http.MethodGet, dlPath, nil)
	rangeReq.Header.Set("Authorization", "Bearer "+f.tokens["admin"])
	rangeReq.Header.Set("Range", "bytes=16-31")
	rangeRec := httptest.NewRecorder()
	dlOK.ServeHTTP(rangeRec, rangeReq)
	if rangeRec.Code != http.StatusPartialContent || !bytes.Equal(rangeRec.Body.Bytes(), payload[16:32]) {
		t.Fatalf("bounded range mismatch: status=%d bytes=%d", rangeRec.Code, rangeRec.Body.Len())
	}
	streamRequest(t, dlOK, http.MethodGet, dlPath, f.tokens["servers"], nil, http.StatusForbidden)
	streamRequest(t, dlOK, http.MethodGet, dlPath, "", nil, http.StatusUnauthorized)
	streamRequest(t, dlNoEnforcer, http.MethodGet, dlPath, f.tokens["admin"], nil, http.StatusForbidden)
	if err := ValidateFamilyStreamSessionID(dlSession.ID); err != nil {
		t.Fatalf("bounded session id rejected: %v", err)
	}
	t.Logf("OSS-374 conformance bytes: upload=%d download=%d range=%d hash=%s",
		len(payload), full.Body.Len(), rangeRec.Body.Len(), wantHash[:12])
}

func TestFamilyStreamConformancePrivateSurfacesStayPrivate(t *testing.T) {
	f := newConfFixture(t)
	baseClients, err := NewClients(f.rec, f.server.URL, f.tokens["admin"])
	if err != nil {
		t.Fatal(err)
	}
	server := NewServer(baseClients)
	confRegisterConformanceTools(server, f.clients(t, "admin"))
	session := connectStdioSession(t, server, context.Background())

	listed, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	names := make(map[string]bool, len(listed.Tools))
	for _, tool := range listed.Tools {
		names[tool.Name] = true
	}
	for _, tc := range confToolCases() {
		if !names[tc.tool] {
			t.Errorf("typed path %s is not reachable via the MCP tool list", tc.tool)
		}
	}
	for _, forbidden := range []string{
		"session", "upload_stream", "download_stream", "oidc", "websocket", "healthz", "readyz",
		"map_proxy", "pack_toml", "metrics_exposition", "openapi",
	} {
		for name := range names {
			if strings.Contains(strings.ToLower(name), forbidden) {
				t.Errorf("raw/private surface exposed as tool %q", name)
			}
		}
	}

	// Byte channels and browser/deployment mechanics stay transport/private,
	// with their exact source reasons still present on the pinned tree.
	for _, route := range []string{
		"PUT /api/v1/upload/{sessionId}", "GET /api/v1/download/{sessionId}",
		"GET /api/v1/worldview/map/{key}/{asset}", "GET /api/v1/packwiz/loaders/{loader}/versions",
		"GET /api/v1/packwiz/{id}/pack.toml", "ANY /api/v1/packwiz/packs/*",
		"GET|POST /api/v1/auth/oidc/{login,callback,exchange}", "/ws",
		"/healthz, /readyz, /health/detail", "gRPC reflection v1 / v1alpha", "/ (frontend catch-all)",
	} {
		surface, ok := FamilyStreamSurfaceFor(route)
		if !ok {
			t.Fatalf("private surface %s is missing from the stream audit", route)
		}
		if surface.IsSupported() {
			t.Errorf("private surface %s was promoted to a supported workflow", route)
		}
		for _, src := range surface.Sources {
			parts := strings.SplitN(src, ":", 3)
			if len(parts) != 3 {
				t.Fatalf("malformed source %q", src)
			}
			raw, err := os.ReadFile(filepath.FromSlash("../../" + parts[0]))
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(string(raw), "\n")
			line, err := strconv.Atoi(parts[1])
			if err != nil || line < 1 || line > len(lines) || !strings.Contains(lines[line-1], parts[2]) {
				t.Errorf("source reason %s no longer matches the pinned tree", src)
			}
		}
	}
	t.Logf("OSS-374 conformance: %d tools listed, %d private surfaces verified as non-tools", len(names), 11)
}
