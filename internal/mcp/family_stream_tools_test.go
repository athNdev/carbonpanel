// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
)

// streamFixture builds real typed Connect clients and real REST handlers in
// front of the production auth interceptor. All state is in-memory or in a
// temp directory: no Docker daemon, live panel, world or user database.
type streamFixture struct {
	store    *storage.Store
	enforcer *rbac.Enforcer
	auth     *auth.Manager
	tokens   map[string]string
	server   *httptest.Server
}

func registerStreamOK[Req, Res any](mux *http.ServeMux, procedure string, ic connect.UnaryInterceptorFunc) {
	mux.Handle(procedure, connect.NewUnaryHandler(procedure,
		func(context.Context, *connect.Request[Req]) (*connect.Response[Res], error) {
			var res Res
			return connect.NewResponse(&res), nil
		}, connect.WithInterceptors(ic)))
}

func newStreamFixture(t *testing.T) *streamFixture {
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

	mux := http.NewServeMux()
	registerAuthHandlers(mux, interceptor, true)

	// Fake typed handlers for every procedure named by the evidence matrix.
	for _, procedure := range []string{
		"/carbonpanel.v1.ConfigService/ValidateProviderKey",
		"/carbonpanel.v1.NodeService/PingNode",
		"/carbonpanel.v1.ModBrowseService/SearchMods",
		"/carbonpanel.v1.ModBrowseService/GetModVersions",
		"/carbonpanel.v1.ModBrowseService/InstallMods",
		"/carbonpanel.v1.MinecraftService/GetLoaderVersions",
		"/carbonpanel.v1.ModpackService/GetModpackConfig",
		"/carbonpanel.v1.ConfigService/GetUpdateBatch",
		"/carbonpanel.v1.ConfigService/UpdateServerConfig",
		"/carbonpanel.v1.WorldViewService/GetWorldViewStatus",
	} {
		switch procedure {
		case "/carbonpanel.v1.ConfigService/ValidateProviderKey":
			registerStreamOK[v1.ValidateProviderKeyRequest, v1.ValidateProviderKeyResponse](mux, procedure, interceptor)
		case "/carbonpanel.v1.NodeService/PingNode":
			registerStreamOK[v1.PingNodeRequest, v1.PingNodeResponse](mux, procedure, interceptor)
		case "/carbonpanel.v1.ModBrowseService/SearchMods":
			registerStreamOK[v1.SearchModsRequest, v1.SearchModsResponse](mux, procedure, interceptor)
		case "/carbonpanel.v1.ModBrowseService/GetModVersions":
			registerStreamOK[v1.GetModVersionsRequest, v1.GetModVersionsResponse](mux, procedure, interceptor)
		case "/carbonpanel.v1.ModBrowseService/InstallMods":
			registerStreamOK[v1.InstallModsRequest, v1.InstallModsResponse](mux, procedure, interceptor)
		case "/carbonpanel.v1.MinecraftService/GetLoaderVersions":
			registerStreamOK[v1.GetLoaderVersionsRequest, v1.GetLoaderVersionsResponse](mux, procedure, interceptor)
		case "/carbonpanel.v1.ModpackService/GetModpackConfig":
			registerStreamOK[v1.GetModpackConfigRequest, v1.GetModpackConfigResponse](mux, procedure, interceptor)
		case "/carbonpanel.v1.ConfigService/GetUpdateBatch":
			registerStreamOK[v1.GetUpdateBatchRequest, v1.GetUpdateBatchResponse](mux, procedure, interceptor)
		case "/carbonpanel.v1.ConfigService/UpdateServerConfig":
			registerStreamOK[v1.UpdateServerConfigRequest, v1.UpdateServerConfigResponse](mux, procedure, interceptor)
		case "/carbonpanel.v1.WorldViewService/GetWorldViewStatus":
			registerStreamOK[v1.GetWorldViewStatusRequest, v1.GetWorldViewStatusResponse](mux, procedure, interceptor)
		default:
			t.Fatalf("no fixture handler for evidence procedure %s", procedure)
		}
	}

	// The public probes used by this lane need no backend state.
	mux.HandleFunc("/healthz", handlers.NewHealthHandler(time.Now(), nil))
	mux.HandleFunc("/api/v1/openapi.yaml", handlers.NewOpenAPIHandler(logger.New(), func() bool { return true }))

	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	f := &streamFixture{store: store, enforcer: enforcer, auth: authManager, tokens: map[string]string{}, server: ts}
	f.tokens["admin"] = seedToken(t, store, enforcer, "u-stream-admin", "admin", nil)
	f.tokens["filesSrv1"] = seedToken(t, store, enforcer, "u-files-1", "stream-files-1", []rbac.Permission{{Resource: "files", Action: "read", ObjectID: "srv-1"}})
	f.tokens["filesSrv2"] = seedToken(t, store, enforcer, "u-files-2", "stream-files-2", []rbac.Permission{{Resource: "files", Action: "read", ObjectID: "srv-2"}})
	f.tokens["uploadsCreator"] = seedToken(t, store, enforcer, "u-uploads", "stream-uploads", globalPerms("uploads", "create"))
	f.tokens["settingsReader"] = seedToken(t, store, enforcer, "u-settings", "stream-settings", globalPerms("settings", "read"))
	f.tokens["serversReader"] = seedToken(t, store, enforcer, "u-servers", "stream-servers", globalPerms("servers", "read"))
	f.tokens["modsReader"] = seedToken(t, store, enforcer, "u-mods-read", "stream-mods-read", globalPerms("mods", "read"))
	f.tokens["configReader"] = seedToken(t, store, enforcer, "u-config-read", "stream-config-read", []rbac.Permission{{Resource: "server_config", Action: "read", ObjectID: "srv-1"}})
	f.tokens["invalid"] = "dp_nonexistent_token"
	return f
}

func (f *streamFixture) clients(t *testing.T, role string) *FamilyStreamClients {
	t.Helper()
	clients, err := NewFamilyStreamClients(f.server.Client(), f.server.URL, f.tokens[role])
	if err != nil {
		t.Fatalf("NewFamilyStreamClients(%s): %v", role, err)
	}
	return clients
}

func streamCallTyped(ctx context.Context, c *FamilyStreamClients, procedure string) error {
	switch procedure {
	case "/carbonpanel.v1.ConfigService/ValidateProviderKey":
		_, err := c.Config.ValidateProviderKey(ctx, connect.NewRequest(&v1.ValidateProviderKeyRequest{Provider: "curseforge"}))
		return err
	case "/carbonpanel.v1.NodeService/PingNode":
		_, err := c.Node.PingNode(ctx, connect.NewRequest(&v1.PingNodeRequest{Id: "node-1"}))
		return err
	case "/carbonpanel.v1.ModBrowseService/SearchMods":
		_, err := c.ModBrowse.SearchMods(ctx, connect.NewRequest(&v1.SearchModsRequest{ServerId: "srv-1", Query: "map"}))
		return err
	case "/carbonpanel.v1.ModBrowseService/GetModVersions":
		_, err := c.ModBrowse.GetModVersions(ctx, connect.NewRequest(&v1.GetModVersionsRequest{ServerId: "srv-1", Project: "map"}))
		return err
	case "/carbonpanel.v1.ModBrowseService/InstallMods":
		_, err := c.ModBrowse.InstallMods(ctx, connect.NewRequest(&v1.InstallModsRequest{ServerId: "srv-1"}))
		return err
	case "/carbonpanel.v1.MinecraftService/GetLoaderVersions":
		_, err := c.Minecraft.GetLoaderVersions(ctx, connect.NewRequest(&v1.GetLoaderVersionsRequest{Loader: "fabric", McVersion: "1.21"}))
		return err
	case "/carbonpanel.v1.ModpackService/GetModpackConfig":
		_, err := c.Modpack.GetModpackConfig(ctx, connect.NewRequest(&v1.GetModpackConfigRequest{Id: "pack-1"}))
		return err
	case "/carbonpanel.v1.ConfigService/GetUpdateBatch":
		_, err := c.Config.GetUpdateBatch(ctx, connect.NewRequest(&v1.GetUpdateBatchRequest{ServerId: "srv-1"}))
		return err
	case "/carbonpanel.v1.ConfigService/UpdateServerConfig":
		_, err := c.Config.UpdateServerConfig(ctx, connect.NewRequest(&v1.UpdateServerConfigRequest{ServerId: "srv-1", Stage: true}))
		return err
	case "/carbonpanel.v1.WorldViewService/GetWorldViewStatus":
		_, err := c.WorldView.GetWorldViewStatus(ctx, connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: "srv-1"}))
		return err
	default:
		return &ToolError{Kind: ErrKindUpstream, Message: "no typed fixture for " + procedure}
	}
}

// streamDenyRoles selects a token that holds an adjacent but insufficient
// grant, so the denial is an authorization decision rather than a malformed
// request. Invalid tokens prove the unauthenticated branch.
var streamDenyRoles = map[string]string{
	"/carbonpanel.v1.ConfigService/ValidateProviderKey":   "serversReader",
	"/carbonpanel.v1.NodeService/PingNode":                "serversReader",
	"/carbonpanel.v1.ModBrowseService/SearchMods":         "serversReader",
	"/carbonpanel.v1.ModBrowseService/GetModVersions":     "serversReader",
	"/carbonpanel.v1.ModBrowseService/InstallMods":        "modsReader",
	"/carbonpanel.v1.MinecraftService/GetLoaderVersions":  "invalid",
	"/carbonpanel.v1.ModpackService/GetModpackConfig":     "serversReader",
	"/carbonpanel.v1.ConfigService/GetUpdateBatch":        "serversReader",
	"/carbonpanel.v1.ConfigService/UpdateServerConfig":    "configReader",
	"/carbonpanel.v1.WorldViewService/GetWorldViewStatus": "serversReader",
}

func streamRequest(t *testing.T, h http.Handler, method, path, token string, body []byte, want int) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != want {
		t.Fatalf("%s %s: status %d want %d: %s", method, path, rec.Code, want, rec.Body.String())
	}
	return rec
}

func TestFamilyStreamAuditIntegrity(t *testing.T) {
	if len(FamilyStreamAudit) != FamilyStreamListedCount {
		t.Fatalf("LISTED count %d, want %d", len(FamilyStreamAudit), FamilyStreamListedCount)
	}
	seen := make(map[string]bool, len(FamilyStreamAudit))
	workflow, transport, private := 0, 0, 0
	reachable := 0
	for _, s := range FamilyStreamAudit {
		if seen[s.Route] {
			t.Fatalf("duplicate route %q", s.Route)
		}
		seen[s.Route] = true
		if s.Reason == "" || s.ACL == "" || len(s.Sources) == 0 {
			t.Errorf("%s: incomplete audit row: %+v", s.Route, s)
		}
		switch s.Decision {
		case FamilyStreamDecisionWorkflow:
			workflow++
			if len(s.TypedEquivalent) == 0 || !s.IsSupported() {
				t.Errorf("%s: supported workflow needs a typed equivalent", s.Route)
			}
		case FamilyStreamDecisionTransport:
			transport++
		case FamilyStreamDecisionPrivate:
			private++
		default:
			t.Errorf("%s: unknown decision %q", s.Route, s.Decision)
		}
		allReachable := true
		for _, src := range s.Sources {
			if !streamSourceAnchorPresent(t, src) {
				allReachable = false
			}
		}
		if allReachable {
			reachable++
		}
	}
	if workflow != FamilyStreamWorkflowCount || transport != FamilyStreamTransportCount || private != FamilyStreamPrivateCount {
		t.Fatalf("decision counts workflow=%d transport=%d private=%d, want %d/%d/%d",
			workflow, transport, private, FamilyStreamWorkflowCount, FamilyStreamTransportCount, FamilyStreamPrivateCount)
	}
	if reachable != FamilyStreamListedCount {
		t.Fatalf("REACHABLE count %d, want %d", reachable, FamilyStreamListedCount)
	}

	if len(FamilyStreamEvidenceMatrix) != FamilyStreamEvidenceCount {
		t.Fatalf("evidence count %d, want %d", len(FamilyStreamEvidenceMatrix), FamilyStreamEvidenceCount)
	}
	evSeen := make(map[string]bool, len(FamilyStreamEvidenceMatrix))
	for _, e := range FamilyStreamEvidenceMatrix {
		if evSeen[e.Route] {
			t.Fatalf("duplicate evidence route %q", e.Route)
		}
		evSeen[e.Route] = true
		surface, ok := FamilyStreamSurfaceFor(e.Route)
		if !ok {
			t.Fatalf("evidence route %q is not listed", e.Route)
		}
		if e.Detail == "" {
			t.Errorf("%s: evidence has no detail", e.Route)
		}
		if e.Check == StreamCheckTypedRPC {
			if e.TypedProcedure == "" {
				t.Errorf("%s: typed_rpc evidence has no procedure", e.Route)
				continue
			}
			if _, ok := Catalog[e.TypedProcedure]; !ok {
				t.Errorf("%s: evidence procedure %s is not in the generated catalog", e.Route, e.TypedProcedure)
			}
			found := false
			for _, p := range surface.TypedEquivalent {
				if p == e.TypedProcedure {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: evidence procedure %s is not a declared typed equivalent", e.Route, e.TypedProcedure)
			}
		}
	}
	for _, s := range FamilyStreamAudit {
		if !evSeen[s.Route] {
			t.Errorf("LISTED surface %s has no fixture evidence row", s.Route)
		}
	}
	t.Logf("OSS-374 LISTED=%d REACHABLE=%d workflow=%d transport=%d private=%d evidence=%d",
		len(FamilyStreamAudit), reachable, workflow, transport, private, len(FamilyStreamEvidenceMatrix))
}

func streamSourceAnchorPresent(t *testing.T, src string) bool {
	t.Helper()
	parts := strings.SplitN(src, ":", 3)
	if len(parts) != 3 {
		t.Errorf("malformed source %q", src)
		return false
	}
	line, err := strconv.Atoi(parts[1])
	if err != nil || line < 1 {
		t.Errorf("malformed source line %q", src)
		return false
	}
	// Test working directory is internal/mcp; the audit paths are repo-rooted.
	raw, err := os.ReadFile(filepath.FromSlash("../../" + parts[0]))
	if err != nil {
		t.Errorf("read %s: %v", parts[0], err)
		return false
	}
	lines := strings.Split(string(raw), "\n")
	if line > len(lines) {
		t.Errorf("source %s has %d lines, anchor wants %d", parts[0], len(lines), line)
		return false
	}
	anchor := parts[2]
	if strings.Contains(lines[line-1], anchor) {
		return true
	}
	// Line numbers are audit metadata, not identity. A preceding edit can shift
	// them without moving the referenced code. Tolerate the shift ONLY when the
	// EXACT anchor string occurs exactly once in the SAME file; a missing or
	// ambiguous anchor still fails closed, so a route moved to wrong code is
	// never accepted as "somewhere in the file".
	matches := make([]int, 0, 1)
	for i, text := range lines {
		idx := strings.Index(text, anchor)
		if idx < 0 {
			continue
		}
		// A prefix such as NewHandlerV1 must not match NewHandlerV1Alpha.
		if rest := text[idx+len(anchor):]; len(rest) > 0 {
			next := rest[0]
			if next == '_' || next >= 'a' && next <= 'z' || next >= 'A' && next <= 'Z' || next >= '0' && next <= '9' {
				continue
			}
		}
		matches = append(matches, i+1)
		if len(matches) > 1 {
			break
		}
	}
	if len(matches) == 0 {
		t.Errorf("%s:%d does not contain %q and the anchor is absent from the file", parts[0], line, anchor)
		return false
	}
	if len(matches) > 1 {
		t.Errorf("%s:%d anchor %q is ambiguous (lines %v); regenerate the audit anchors", parts[0], line, anchor, matches)
		return false
	}
	t.Logf("audit anchor %s shifted from line %d to %d (exact anchor still unique)", parts[0], line, matches[0])
	return true
}

func TestFamilyStreamTypedEquivalentsAreMountedAndScoped(t *testing.T) {
	mounted := make(map[string]bool, len(MountedServices))
	for _, s := range MountedServices {
		mounted[s] = true
	}
	for _, procedure := range FamilyStreamTypedProcedures() {
		entry, ok := Catalog[procedure]
		if !ok {
			t.Errorf("%s: typed equivalent is not in the generated catalog", procedure)
			continue
		}
		if !mounted[entry.Service[strings.LastIndex(entry.Service, ".")+1:]] {
			t.Errorf("%s: typed equivalent service is not mounted", procedure)
		}
		if want := expectedScope(procedure); entry.Scope != want {
			t.Errorf("%s: catalog scope %q, live rbac %q", procedure, entry.Scope, want)
		}
		for _, fragment := range []string{"NodeControlService", "SendCommand", "ScanNodes"} {
			if strings.Contains(procedure, fragment) {
				t.Errorf("excluded procedure leaked into typed equivalents: %s", procedure)
			}
		}
		if strings.Contains(procedure, "UploadService") {
			// OSS-375 classifies these as SUPPORTED typed equivalents of the
			// excluded raw byte channel. The integration catalog promotes them
			// as bounded, confirm-gated RPC tools (base64, 256 KiB frames), not
			// as raw HTTP/byte-surface tools, so they must be reachable.
			op, ok := FamilyAuditOperationByProcedure(procedure)
			if !ok || !op.Supported() {
				t.Errorf("%s: upload typed equivalent must remain audit-supported", procedure)
			}
			if entry.Class == ClassExclude {
				t.Errorf("%s: audit-supported upload tool must not stay excluded", procedure)
			}
		}
	}
}

func TestFamilyStreamNoRawSurfaceTools(t *testing.T) {
	for _, forbidden := range []string{
		"/api/", "sessionid", "worldview/map", "oidc", "healthz", "readyz", "/ws", "pack.toml", "raw_url",
	} {
		for _, spec := range Tools {
			// Tool identity is the name and its typed procedure. Descriptions
			// may cite the documented equivalent flow from the audit matrix
			// (for example the scoped GET behind init_file_download); that is
			// documentation, not a raw transport tool.
			haystack := strings.ToLower(spec.Name + " " + spec.Procedure)
			if strings.Contains(haystack, strings.ToLower(forbidden)) {
				t.Errorf("tool %s exposes raw surface fragment %q", spec.Name, forbidden)
			}
		}
	}
	for _, raw := range []string{
		"https://node.example/api/v1/download/abcd", "/api/v1/upload/session-1", "../session", "a?b=c", "a#frag", "a\\b",
	} {
		if err := ValidateFamilyStreamSessionID(raw); err == nil {
			t.Errorf("raw session handle %q was accepted", raw)
		}
	}
	if err := ValidateFamilyStreamSessionID("3f4e5d6c-7b8a-49ab-9cde-0f1e2d3c4b5a"); err != nil {
		t.Fatalf("opaque session handle was rejected: %v", err)
	}
}

func TestFamilyStreamTypedPathSuccessAndStructuredDenial(t *testing.T) {
	f := newStreamFixture(t)
	ctx := context.Background()
	verified := 0
	for _, e := range FamilyStreamEvidenceMatrix {
		if e.Check != StreamCheckTypedRPC {
			continue
		}
		spec, ok := FamilyStreamToolSpec(e.TypedProcedure)
		if !ok {
			t.Fatalf("no tool spec for %s", e.TypedProcedure)
		}
		t.Run(e.TypedProcedure+"/success", func(t *testing.T) {
			if err := streamCallTyped(ctx, f.clients(t, "admin"), e.TypedProcedure); err != nil {
				t.Fatalf("typed success %s: %v", e.TypedProcedure, err)
			}
		})
		denyRole, ok := streamDenyRoles[e.TypedProcedure]
		if !ok {
			t.Fatalf("no denial role for %s", e.TypedProcedure)
		}
		t.Run(e.TypedProcedure+"/structured_denial", func(t *testing.T) {
			err := streamCallTyped(ctx, f.clients(t, denyRole), e.TypedProcedure)
			if err == nil {
				t.Fatalf("typed path %s succeeded for insufficient grant", e.TypedProcedure)
			}
			te := toToolError(spec, err)
			if te.Tool != e.TypedProcedure || te.Procedure != e.TypedProcedure || te.Code == "" {
				t.Fatalf("incomplete structured denial: %+v", te)
			}
			if te.Kind != ErrKindPermissionDenied && te.Kind != ErrKindUnauthenticated {
				t.Fatalf("unexpected denial kind %q for %s", te.Kind, e.TypedProcedure)
			}
		})
		verified++
	}
	t.Logf("OSS-374 typed-path VERIFIED=%d", verified)
}

func TestFamilyStreamScopedStreamingHandlers(t *testing.T) {
	f := newStreamFixture(t)
	log := logger.New()

	t.Run("upload", func(t *testing.T) {
		manager := pkgupload.NewManager(t.TempDir(), time.Hour, 1<<20, log)
		t.Cleanup(manager.Stop)
		session, err := manager.InitSession("fixture.bin", 4, 4)
		if err != nil {
			t.Fatalf("InitSession: %v", err)
		}
		if err := ValidateFamilyStreamSessionID(session.ID); err != nil {
			t.Fatalf("opaque upload session id rejected: %v", err)
		}
		handler := handlers.NewUploadStreamHandler(manager, f.auth, f.enforcer, log)
		noEnforcer := handlers.NewUploadStreamHandler(manager, f.auth, nil, log)
		path := "/api/v1/upload/" + session.ID
		rec := streamRequest(t, handler, http.MethodPut, path, f.tokens["uploadsCreator"], []byte("abcd"), http.StatusOK)
		var body struct {
			Completed bool `json:"completed"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.Completed {
			t.Fatalf("upload completion not reported: %s (%v)", rec.Body.String(), err)
		}
		streamRequest(t, handler, http.MethodPut, path, f.tokens["filesSrv1"], []byte("abcd"), http.StatusForbidden)
		streamRequest(t, handler, http.MethodPut, path, "", []byte("abcd"), http.StatusUnauthorized)
		streamRequest(t, noEnforcer, http.MethodPut, path, f.tokens["uploadsCreator"], []byte("abcd"), http.StatusForbidden)
	})

	t.Run("download", func(t *testing.T) {
		file := filepath.Join(t.TempDir(), "world.zip")
		payload := []byte("world-bytes")
		if err := os.WriteFile(file, payload, 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		manager := pkgdownload.NewManager(t.TempDir(), time.Hour, log)
		t.Cleanup(manager.Stop)
		session := manager.InitSessionFor(file, "world.zip", int64(len(payload)), false, "srv-1", "u-files-1")
		if err := ValidateFamilyStreamSessionID(session.ID); err != nil {
			t.Fatalf("opaque download session id rejected: %v", err)
		}
		handler := handlers.NewDownloadStreamHandler(manager, f.auth, f.enforcer, log)
		noEnforcer := handlers.NewDownloadStreamHandler(manager, f.auth, nil, log)
		path := "/api/v1/download/" + session.ID
		rec := streamRequest(t, handler, http.MethodGet, path, f.tokens["filesSrv1"], nil, http.StatusOK)
		if !bytes.Equal(rec.Body.Bytes(), payload) {
			t.Fatalf("download bytes mismatched: %q", rec.Body.String())
		}
		streamRequest(t, handler, http.MethodGet, path, f.tokens["filesSrv2"], nil, http.StatusForbidden)
		streamRequest(t, handler, http.MethodGet, path, "", nil, http.StatusUnauthorized)
		streamRequest(t, noEnforcer, http.MethodGet, path, f.tokens["filesSrv1"], nil, http.StatusForbidden)
	})

	t.Run("key_validator", func(t *testing.T) {
		handler := handlers.NewKeyValidatorHandler(f.auth, f.enforcer, log)
		body, err := json.Marshal(handlers.ValidateKeyRequest{Provider: "unsupported-provider", APIKey: "fixture"})
		if err != nil {
			t.Fatal(err)
		}
		streamRequest(t, handler, http.MethodPost, "/api/v1/settings/validate-key", f.tokens["settingsReader"], body, http.StatusOK)
		streamRequest(t, handler, http.MethodPost, "/api/v1/settings/validate-key", f.tokens["serversReader"], body, http.StatusForbidden)
		streamRequest(t, handler, http.MethodPost, "/api/v1/settings/validate-key", "", body, http.StatusUnauthorized)
	})

	t.Run("node_scan", func(t *testing.T) {
		// Denied requests never invoke the scanner, so no Docker probe occurs.
		handler := handlers.NewNodeScanHandler(f.store, f.auth, f.enforcer, log)
		streamRequest(t, handler, http.MethodGet, "/api/v1/nodes/scan", f.tokens["serversReader"], nil, http.StatusForbidden)
		streamRequest(t, handler, http.MethodGet, "/api/v1/nodes/scan", "", nil, http.StatusUnauthorized)
	})

	t.Run("mod_search", func(t *testing.T) {
		manager := handlers.NewModOnlineManager(f.store, log, f.auth, f.enforcer)
		streamRequest(t, manager, http.MethodGet, "/api/v1/servers/srv-1/mods/search?query=x", f.tokens["serversReader"], nil, http.StatusForbidden)
		streamRequest(t, manager, http.MethodGet, "/api/v1/servers/srv-1/mods/search?query=x", "", nil, http.StatusUnauthorized)
	})

	t.Run("packwiz_studio", func(t *testing.T) {
		handler := handlers.NewPackwizHandler(nil, f.store, nil, log, f.auth, f.enforcer, &config.Config{})
		streamRequest(t, handler, http.MethodGet, "/api/v1/packwiz/packs", f.tokens["serversReader"], nil, http.StatusForbidden)
		streamRequest(t, handler, http.MethodGet, "/api/v1/packwiz/packs", "", nil, http.StatusUnauthorized)
	})

	t.Run("staged_config", func(t *testing.T) {
		handler := handlers.NewStagedConfigHandler(f.store, log, f.auth, f.enforcer)
		streamRequest(t, handler, http.MethodGet, "/api/v1/staged-config/srv-1/staged", f.tokens["configReader"], nil, http.StatusOK)
		streamRequest(t, handler, http.MethodGet, "/api/v1/staged-config/srv-1/staged", f.tokens["serversReader"], nil, http.StatusForbidden)
		streamRequest(t, handler, http.MethodGet, "/api/v1/staged-config/srv-1/staged", "", nil, http.StatusUnauthorized)
		// apply/rollback/discard are private operator maintenance: a read-only
		// grant is denied before any staged-record lookup or container change.
		streamRequest(t, handler, http.MethodPost, "/api/v1/staged-config/srv-1/staged/stg-1/apply", f.tokens["configReader"], nil, http.StatusForbidden)
	})

	t.Run("metrics", func(t *testing.T) {
		handler := handlers.NewMetricsHandler(f.store, nil, f.auth, f.enforcer, log)
		streamRequest(t, handler, http.MethodGet, "/api/v2/metrics/host", f.tokens["serversReader"], nil, http.StatusOK)
		streamRequest(t, handler, http.MethodGet, "/api/v2/metrics/host", f.tokens["filesSrv1"], nil, http.StatusForbidden)
		// The metrics adapter collapses every authorization failure (including
		// missing credentials) into 403; it is still fail-closed.
		streamRequest(t, handler, http.MethodGet, "/api/v2/metrics/host", "", nil, http.StatusForbidden)
		streamRequest(t, handler, http.MethodGet, "/api/v2/metrics/servers/srv-1", f.tokens["serversReader"], nil, http.StatusOK)
		streamRequest(t, handler, http.MethodGet, "/api/v2/metrics/servers/srv-1", f.tokens["filesSrv1"], nil, http.StatusForbidden)
	})

	t.Run("public_probes", func(t *testing.T) {
		// The OpenAPI spec may not be built in a unit-test checkout; either
		// "present" or "not deployed" proves it needs no credential.
		for _, probe := range []struct {
			path string
			ok   []int
		}{
			{"/healthz", []int{http.StatusOK}},
			{"/api/v1/openapi.yaml", []int{http.StatusOK, http.StatusNotFound}},
		} {
			req, err := http.NewRequest(http.MethodGet, probe.path, nil)
			if err != nil {
				t.Fatal(err)
			}
			rec := httptest.NewRecorder()
			f.server.Config.Handler.ServeHTTP(rec, req)
			allowed := false
			for _, code := range probe.ok {
				if rec.Code == code {
					allowed = true
				}
			}
			if !allowed {
				t.Fatalf("%s status %d: %s", probe.path, rec.Code, rec.Body.String())
			}
		}
	})
}

func TestFamilyStreamSourceOnlySurfacesHaveNoTool(t *testing.T) {
	// These surfaces are private by construction (browser handshakes, static
	// assets, stale reflection). Their evidence is the verified mount plus the
	// guarantee that no promoted tool exposes them. The remaining raw surfaces
	// are verified by the typed-path and stream-handler fixtures above.
	for _, e := range FamilyStreamEvidenceMatrix {
		if e.Check != StreamCheckSource {
			continue
		}
		surface, ok := FamilyStreamSurfaceFor(e.Route)
		if !ok {
			t.Fatalf("source surface %s is not listed", e.Route)
		}
		if surface.IsSupported() || e.Success || e.Denied {
			t.Errorf("source-only surface %s was promoted or claims a fixture it does not have", e.Route)
		}
		for _, src := range surface.Sources {
			streamSourceAnchorPresent(t, src)
		}
	}
}
