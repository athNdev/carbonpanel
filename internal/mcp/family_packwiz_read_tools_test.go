// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/config"
	"github.com/athNdev/carbon-panel/internal/packwiz"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc"
	"github.com/athNdev/carbon-panel/internal/rpc/handlers"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/pkg/logger"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

const packwizFixtureID = "pack-alpha"

func registerPackwizReadFixture(t *testing.T, mux *http.ServeMux, authorize connect.UnaryInterceptorFunc, olderMode string) *packwiz.Manager {
	t.Helper()
	if olderMode != "" {
		return nil // Older panels do not register the generated service.
	}
	manager := packwiz.NewManager(t.TempDir(), logger.New())
	mod := packwiz.ModItem{
		Slug: "sample-mod", Name: "Sample mod", FileName: "fixture-mod.jar", Side: "both",
		Platform: "modrinth", ProjectID: "sample-project", VersionID: "sample-v1",
		DownloadURL: "https://packages.invalid/fixture.jar?signature=fixture-download-signature",
		Pinned:      true,
	}
	datapack := packwiz.ModItem{
		Slug: "sample-data", Name: "Sample datapack", FileName: "fixture-data.zip", Side: "server",
	}
	for _, p := range []*packwiz.Pack{
		{
			ID: packwizFixtureID, Name: "Alpha", Mods: []packwiz.ModItem{mod},
			Datapacks: []packwiz.ModItem{datapack}, DatapackFolder: "fixture-custom-folder",
			Options: map[string]any{"fixture_private_option": "fixture-option-value"},
		},
		{ID: "pack-beta", Name: "Beta"},
	} {
		if _, err := manager.CreatePack(p); err != nil {
			t.Fatalf("create fixture pack: %v", err)
		}
	}
	packService := services.NewPackwizService(handlers.NewPackwizOperations(manager))
	path, handler := carbonpanelv1connect.NewPackwizServiceHandler(
		packService, connect.WithInterceptors(authorize, recordingStreamInterceptor()),
	)
	mux.Handle(path, handler)
	return manager
}

func TestPackwizReadsGlobalScope(t *testing.T) {
	ctx := context.Background()
	store := newTestStore(t)
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatal(err)
	}
	authManager, err := auth.NewManager(store, enforcer, &config.AuthConfig{Local: config.LocalConfig{Enabled: true}})
	if err != nil {
		t.Fatal(err)
	}
	authorize := rpc.NewAuthInterceptor(authManager, enforcer, store, logger.New())
	mux := http.NewServeMux()
	registerAuthHandlers(mux, authorize, true)
	manager := registerPackwizReadFixture(t, mux, authorize, "")
	panel := httptest.NewServer(mux)
	t.Cleanup(panel.Close)

	session := func(role string, grants []rbac.Permission) *sdkmcp.ClientSession {
		t.Helper()
		token := seedToken(t, store, enforcer, role, role, grants)
		clients, err := NewClients(panel.Client(), panel.URL, token)
		if err != nil {
			t.Fatal(err)
		}
		if err := clients.Verify(ctx); err != nil {
			t.Fatal(err)
		}
		return connectStdioSession(t, NewServer(clients), ctx)
	}
	reader := session("packwiz-reader", globalPerms(rbac.ResourceModpacks, rbac.ActionRead))
	objectOnly := session("packwiz-object-reader", []rbac.Permission{
		{Resource: rbac.ResourceModpacks, Action: rbac.ActionRead, ObjectID: packwizFixtureID},
	})
	noGrant := session("packwiz-no-grant", nil)
	listArgs := map[string]any{"offset": 0, "limit": 1}
	getArgs := map[string]any{"pack_id": packwizFixtureID, "offset": 1, "limit": 1}
	for _, tc := range []struct {
		tool string
		args map[string]any
	}{{packwizListTool, listArgs}, {packwizGetTool, getArgs}} {
		for _, denied := range []*sdkmcp.ClientSession{objectOnly, noGrant} {
			err := assertStructuredError(t, callTool(t, denied, tc.tool, tc.args), tc.tool, ErrKindPermissionDenied)
			if err.Code != connect.CodePermissionDenied.String() {
				t.Fatalf("%s denial code = %q", tc.tool, err.Code)
			}
		}
	}

	listed := callTool(t, reader, packwizListTool, listArgs)
	assertSuccess(t, listed, packwizListTool)
	result := listed.Result.(map[string]any)
	packs := result["packs"].([]any)
	if len(packs) != 1 || packs[0].(map[string]any)["id"] != packwizFixtureID ||
		result["total"] != float64(2) || result["next_offset"] != float64(1) {
		t.Fatalf("list page: %+v", result)
	}
	detail := callTool(t, reader, packwizGetTool, getArgs)
	assertSuccess(t, detail, packwizGetTool)
	result = detail.Result.(map[string]any)
	content := result["content"].([]any)
	if len(content) != 1 || content[0].(map[string]any)["kind"] != "datapack" ||
		result["total"] != float64(2) || result["next_offset"] != nil {
		t.Fatalf("detail page: %+v", result)
	}

	all := callTool(t, reader, packwizGetTool, map[string]any{"pack_id": packwizFixtureID})
	assertSuccess(t, all, packwizGetTool)
	content = all.Result.(map[string]any)["content"].([]any)
	mod := content[0].(map[string]any)
	if mod["kind"] != "mod" || mod["project_id"] != "sample-project" || mod["pinned"] != true {
		t.Fatalf("projected content: %+v", mod)
	}
	data, err := json.Marshal(all.Result)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"download_url", "fixture-download-signature", "fixture-mod.jar", "fixture-option-value", "fixture-custom-folder", "options", "datapack_folder"} {
		if strings.Contains(string(data), secret) {
			t.Errorf("Packwiz read disclosed %q in %s", secret, data)
		}
	}
	_ = assertStructuredError(t, callTool(t, reader, packwizGetTool, map[string]any{"pack_id": "missing"}), packwizGetTool, ErrKindNotFound)
	_ = assertStructuredError(t, callTool(t, reader, packwizGetTool, map[string]any{"pack_id": "../pack-alpha"}), packwizGetTool, ErrKindInvalidArguments)
	_ = assertStructuredError(t, callTool(t, reader, packwizListTool, map[string]any{"limit": 51}), packwizListTool, ErrKindInvalidArguments)

	if _, err := manager.CreatePack(&packwiz.Pack{
		ID: "large-pack", Options: map[string]any{"large": strings.Repeat("x", packwizReadMaxBytes)},
	}); err != nil {
		t.Fatal(err)
	}
	limited := callTool(t, reader, packwizGetTool, map[string]any{"pack_id": "large-pack"})
	if err := assertStructuredError(t, limited, packwizGetTool, ErrKindUpstream); err.Code != connect.CodeResourceExhausted.String() {
		t.Fatalf("oversized response error: %+v", err)
	}
}

func TestPackwizReadsAgainstOlderController(t *testing.T) {
	f := newFixture(t, "unimplemented", true)
	for _, tool := range []string{packwizListTool, packwizGetTool} {
		out := callTool(t, f.sessions["admin"], tool, contentValidArgs()[tool])
		if err := assertStructuredError(t, out, tool, ErrKindCapability); err.MinimumPanelBuild != string(PanelBuildProductRPCs) {
			t.Fatalf("%s minimum build: %+v", tool, err)
		}
	}
}
