// SPDX-License-Identifier: MPL-2.0

package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"connectrpc.com/connect"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/worldview"
	"github.com/athNdev/carbon-panel/pkg/files"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

func TestNativeWorldViewEndThroughScopedProxy(t *testing.T) {
	root := os.Getenv("CARBON_TERRAIN_END_FIXTURE_ROOT")
	level := os.Getenv("CARBON_TERRAIN_END_FIXTURE_LEVEL")
	if root == "" || level == "" {
		t.Skip("mounted End qualification requires a stopped disposable Java 1.21.9 End fixture")
	}
	paths, err := filepath.Glob(filepath.Join(root, level, "DIM1", "region", "r.*.mca"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no generated End regions in fixture: %v", err)
	}
	overworldRegions, err := filepath.Glob(filepath.Join(root, level, "region", "r.*.mca"))
	if err != nil || len(overworldRegions) == 0 {
		t.Fatalf("no generated Overworld regions in fixture: %v", err)
	}
	paths = append(paths, overworldRegions...)
	hashes := make([]string, len(paths))
	for i, path := range paths {
		hashes[i], err = files.SHA256File(path)
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for i, path := range paths {
			sum, err := files.SHA256File(path)
			if err != nil || sum != hashes[i] {
				t.Errorf("mounted End viewing changed the fixture source bytes: %v", err)
			}
		}
	})

	ctx := context.Background()
	srv, ts := buildHarness(t, false, false, nil, false)
	server := &storage.Server{ID: "end-map-fixture", Name: "End fixture", NodeID: "local", DataPath: root, ModLoader: storage.ModLoaderVanilla}
	if err := srv.store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	cfg := &storage.ServerConfig{ID: server.ID, ServerID: server.ID, Level: &level}
	if err := srv.store.SaveServerConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}

	native := worldview.NewNativeMap(srv.store, nodestorage.NewResolver(nil))
	wv := worldview.NewService(worldview.ServiceDeps{Store: srv.store, Authorizer: srv.enforcer, Native: native})
	path, handler := carbonpanelv1connect.NewWorldViewServiceHandler(wv, connect.WithInterceptors(srv.authInterceptor()))
	mux := ts.Config.Handler.(*http.ServeMux)
	mux.Handle(path, handler)
	mux.Handle(worldview.ProxyPrefix, worldview.NewProxyHandler(worldview.ProxyDeps{
		Auth: srv.authManager, Authorizer: srv.enforcer, Subusers: srv.store, Servers: srv.store,
		Sessions: wv.SessionRegistry(), Native: native,
	}))
	api := carbonpanelv1connect.NewWorldViewServiceClient(ts.Client(), ts.URL)
	readPermission := []rbac.Permission{{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: server.ID}}
	owner := seedWorldViewToken(t, srv, "end-map-reader", "end-map-role", readPermission)
	other := seedWorldViewToken(t, srv, "another-end-reader", "another-end-role", readPermission)

	issue := func() string {
		t.Helper()
		req := connect.NewRequest(&v1.GetWorldViewSessionRequest{ServerId: server.ID})
		req.Header().Set("Authorization", "Bearer "+owner)
		result, err := api.GetWorldViewSession(ctx, req)
		if err != nil || result.Msg.GetState() != v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE || result.Msg.GetProxyPath() == "" {
			t.Fatalf("End fixture did not get a native session: %v (%v)", result, err)
		}
		return result.Msg.GetProxyPath()
	}
	session := issue()
	asset := func(token, name string, want int) ([]byte, http.Header) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+session+name, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "cp_session", Value: token})
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = resp.Body.Close() }()
		body, err := io.ReadAll(resp.Body)
		if err != nil || resp.StatusCode != want {
			t.Fatalf("%s status %d, want %d (%s, %v)", name, resp.StatusCode, want, body, err)
		}
		return body, resp.Header
	}

	const endTile = "native/end/0/0.png"
	asset("", endTile, http.StatusUnauthorized)
	asset(other, endTile, http.StatusForbidden)
	manifest, _ := asset(owner, "native/manifest.json", http.StatusOK)
	var decoded struct {
		TileBlocks int
		Dimension  string
		Dimensions []string
		Spawn      *struct{ X, Z int }
	}
	if err := json.Unmarshal(manifest, &decoded); err != nil || decoded.TileBlocks != 128 ||
		decoded.Dimension != "overworld" || decoded.Spawn == nil || !slices.Equal(decoded.Dimensions, []string{"overworld", "end"}) {
		t.Fatalf("qualified End manifest = %s (%v)", manifest, err)
	}
	body, header := asset(owner, endTile, http.StatusOK)
	dimensions, err := png.DecodeConfig(bytes.NewReader(body))
	rendered, countErr := strconv.Atoi(header.Get("X-CarbonPanel-Rendered-Chunks"))
	if err != nil || countErr != nil || dimensions.Width != 128 || dimensions.Height != 128 ||
		rendered < 1 || header.Get("X-CarbonPanel-Tile-Saved-At") == "" ||
		header.Get("Content-Type") != "image/png" || header.Get("Cache-Control") != "private, no-store, no-cache, must-revalidate, max-age=0" {
		t.Fatalf("qualified End tile invalid: %+v, %v (%v, %v)", dimensions, header, err, countErr)
	}
	tileAt := func(block int) string {
		tile := block / decoded.TileBlocks
		if block%decoded.TileBlocks < 0 {
			tile--
		}
		return strconv.Itoa(tile)
	}
	overworldAsset := "native/overworld/" + tileAt(decoded.Spawn.X) + "/" + tileAt(decoded.Spawn.Z) + ".png"
	overworld, _ := asset(owner, overworldAsset, http.StatusOK)
	if bytes.Equal(body, overworld) {
		t.Fatal("the End and Overworld resolved to the same raster")
	}
	asset(owner, "native/end/7/7.png", http.StatusNotFound)
	asset(owner, "native/nether/0/0.png", http.StatusNotFound)
	t.Logf("End tile served %d saved chunks from the real fixture through the scoped PNG route", rendered)

	server.ModLoader = storage.ModLoaderPaper
	if err := srv.store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	asset(owner, endTile, http.StatusConflict)
	session = issue()
	manifest, _ = asset(owner, "native/manifest.json", http.StatusOK)
	if bytes.Contains(manifest, []byte(`"dimensions"`)) {
		t.Fatalf("unqualified loader advertised End: %s", manifest)
	}
	asset(owner, endTile, http.StatusUnprocessableEntity)
	asset(owner, overworldAsset, http.StatusOK)
}
