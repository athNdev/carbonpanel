// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package rpc

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/rpc/services"
	"github.com/athNdev/carbon-panel/internal/worldlib"
	"github.com/athNdev/carbon-panel/internal/worldview"
	"github.com/athNdev/carbon-panel/pkg/files"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

func TestNativeWorldViewSessionReadsTerrainThroughScopedProxy(t *testing.T) {
	ctx := context.Background()
	srv, ts := buildHarness(t, false, false, nil, false)
	const nodeID = "5dc861be-e051-4e41-8a56-37619d2e5ce4"
	if err := srv.store.CreateNode(ctx, &storage.Node{
		ID: nodeID, Name: "Controller local", Host: "unix:///var/run/docker.sock", Enabled: true, IsLocal: true,
	}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	level := "active_overworld"
	if err := os.MkdirAll(filepath.Join(root, level, "region"), 0o755); err != nil {
		t.Fatal(err)
	}
	levelNBT, err := worldlib.WriteFreshLevelData(map[string]string{"level-name": level}, "3953")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, level, "level.dat"), levelNBT, 0o644); err != nil {
		t.Fatal(err)
	}
	regionPath := filepath.Join(root, level, "region", "r.0.0.mca")
	region := nativeSparseRegion(t)
	if err := os.WriteFile(regionPath, region, 0o644); err != nil {
		t.Fatal(err)
	}
	server := &storage.Server{ID: "native-map-server", Name: "Map fixture", NodeID: nodeID, DataPath: root}
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
	granted := seedWorldViewToken(t, srv, "native-map-reader", "native-map-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: server.ID},
	})
	unrelated := seedWorldViewToken(t, srv, "unrelated-user", "unrelated-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: "another-server"},
	})

	status := connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: server.ID})
	status.Header().Set("Authorization", "Bearer "+granted)
	observation, err := api.GetWorldViewStatus(ctx, status)
	if err != nil || observation.Msg.GetTarget().GetRendererType() != v1.RendererType_RENDERER_TYPE_NATIVE ||
		observation.Msg.GetTarget().GetRendererState() != v1.RendererState_RENDERER_STATE_NATIVE_AVAILABLE ||
		observation.Msg.GetTarget().GetWorldRef().GetLevelName() != level ||
		observation.Msg.GetTarget().GetLastRender() != nil {
		t.Fatalf("native target did not report the active scoped world: %v, %v", observation, err)
	}
	if !slices.Contains(observation.Msg.GetTarget().GetRenderer().GetCapabilities(), worldview.CapabilityMarkers) {
		t.Fatal("native renderer descriptor omitted saved markers")
	}
	capRequest := connect.NewRequest(&v1.ListCapabilitiesRequest{ServerId: server.ID})
	capRequest.Header().Set("Authorization", "Bearer "+granted)
	caps, err := api.ListCapabilities(ctx, capRequest)
	if err != nil || caps.Msg.GetRendererType() != v1.RendererType_RENDERER_TYPE_NATIVE ||
		!slices.ContainsFunc(caps.Msg.GetCapabilities(), func(cap *v1.Capability) bool {
			return cap.GetName() == worldview.CapabilityMarkers && cap.GetSupported()
		}) {
		t.Fatalf("native capabilities omitted saved markers: %v, %v", caps, err)
	}
	issue := func(token string, ref *v1.WorldRef) (*v1.GetWorldViewSessionResponse, error) {
		t.Helper()
		req := connect.NewRequest(&v1.GetWorldViewSessionRequest{ServerId: server.ID, WorldRef: ref})
		req.Header().Set("Authorization", "Bearer "+token)
		resp, err := api.GetWorldViewSession(ctx, req)
		if err != nil {
			return nil, err
		}
		return resp.Msg, nil
	}
	session, err := issue(granted, nil)
	if err != nil || session.GetProxyPath() == "" {
		t.Fatalf("no native map session: %v, %v", session, err)
	}
	if _, err := issue(unrelated, nil); connect.CodeOf(err) != connect.CodePermissionDenied {
		t.Fatalf("cross-server session should be denied, got %v", err)
	}
	forged, err := issue(granted, &v1.WorldRef{LevelName: "inactive_overworld"})
	if err != nil || forged.GetProxyPath() != "" {
		t.Fatalf("inactive world unexpectedly got a map session: %v, %v", forged, err)
	}

	asset := func(token, path string, want int) ([]byte, http.Header) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, ts.URL+session.GetProxyPath()+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.AddCookie(&http.Cookie{Name: "cp_session", Value: token})
		}
		res, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = res.Body.Close() }()
		body, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("%s status = %d, want %d: %s (%v)", path, res.StatusCode, want, body, err)
		}
		return body, res.Header
	}

	const tileAsset = "native/overworld/0/0.png"
	asset("", tileAsset, http.StatusUnauthorized)
	asset(unrelated, tileAsset, http.StatusForbidden)
	manifest, _ := asset(granted, "native/manifest.json", http.StatusOK)
	if !bytes.Contains(manifest, []byte(`"tileBlocks":128`)) || !bytes.Contains(manifest, []byte(`"dimension":"overworld"`)) ||
		bytes.Contains(manifest, []byte(`"dimensions"`)) {
		t.Fatalf("native manifest did not describe the tile scale: %s", manifest)
	}
	body, header := asset(granted, tileAsset, http.StatusOK)
	info, err := png.DecodeConfig(bytes.NewReader(body))
	if err != nil || info.Width != 128 || info.Height != 128 || header.Get("Content-Type") != "image/png" ||
		header.Get("X-CarbonPanel-Rendered-Chunks") != "1" || header.Get("X-CarbonPanel-Provisional-Chunks") != "0" || header.Get("X-CarbonPanel-Tile-Saved-At") == "" ||
		header.Get("Cache-Control") != "private, no-store, no-cache, must-revalidate, max-age=0" {
		t.Fatalf("protected tile response invalid: %+v, %v, %v", info, header, err)
	}
	asset(granted, "native/overworld/1/0.png", http.StatusNotFound)
	asset(granted, "native/nether/0/0.png", http.StatusNotFound)

	observed, err := os.ReadFile(regionPath)
	if err != nil || !bytes.Equal(observed, region) {
		t.Fatalf("native viewing changed the world fixture: %v", err)
	}

	// An upgraded level may still contain chunks written by the old game.
	// The assets must agree when level.dat declares an unsupported version.
	levelPath := filepath.Join(root, level, "level.dat")
	newerMetadata, err := worldlib.WriteFreshLevelData(map[string]string{"level-name": level}, "9999")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(levelPath, newerMetadata, 0o644); err != nil {
		t.Fatal(err)
	}
	unsupportedReq := connect.NewRequest(&v1.GetWorldViewStatusRequest{ServerId: server.ID})
	unsupportedReq.Header().Set("Authorization", "Bearer "+granted)
	unsupported, err := api.GetWorldViewStatus(ctx, unsupportedReq)
	if err != nil || unsupported.Msg.GetTarget().GetRendererState() != v1.RendererState_RENDERER_STATE_UNSUPPORTED ||
		unsupported.Msg.GetTarget().GetError() == "" {
		t.Fatalf("unsupported save was advertised as ready: %v, %v", unsupported, err)
	}

	unavailable, err := issue(granted, nil)
	if err != nil || unavailable.GetProxyPath() != "" || unavailable.GetState() != v1.RendererState_RENDERER_STATE_UNSUPPORTED ||
		unavailable.GetReason() == "" {
		t.Fatalf("unsupported save received a session or no reason: %v, %v", unavailable, err)
	}

	unavailableCapsReq := connect.NewRequest(&v1.ListCapabilitiesRequest{ServerId: server.ID})
	unavailableCapsReq.Header().Set("Authorization", "Bearer "+granted)
	unavailableCaps, err := api.ListCapabilities(ctx, unavailableCapsReq)
	if err != nil || slices.ContainsFunc(unavailableCaps.Msg.GetCapabilities(), func(cap *v1.Capability) bool {
		return cap.GetSupported()
	}) {
		t.Fatalf("unsupported save advertised native capabilities: %v, %v", unavailableCaps, err)
	}

	fleet := seedWorldViewToken(t, srv, "native-map-fleet", "native-map-fleet-role", []rbac.Permission{
		{Resource: rbac.ResourceWorldView, Action: rbac.ActionRead, ObjectID: "*"},
	})
	listReq := connect.NewRequest(&v1.ListWorldViewTargetsRequest{})
	listReq.Header().Set("Authorization", "Bearer "+fleet)
	list, err := api.ListWorldViewTargets(ctx, listReq)
	if err != nil || len(list.Msg.GetTargets()) != 1 ||
		list.Msg.GetTargets()[0].GetRendererState() != v1.RendererState_RENDERER_STATE_UNSUPPORTED {
		t.Fatalf("inventory advertised an unsupported native save: %v, %v", list, err)
	}
	asset(granted, "native/manifest.json", http.StatusUnprocessableEntity)
	asset(granted, tileAsset, http.StatusUnprocessableEntity)
	if err := os.WriteFile(levelPath, levelNBT, 0o644); err != nil {
		t.Fatal(err)
	}

	// A real BackupService restore publishes new bytes under the identical
	// server, node and level name without a World Library binding.
	backup := services.NewBackupService(srv.store, nil, nil, config.S3Config{}, srv.log)
	backup.SetEnforcer(srv.enforcer)
	backupPath, backupHandler := carbonpanelv1connect.NewBackupServiceHandler(backup, connect.WithInterceptors(srv.authInterceptor()))
	mux.Handle(backupPath, backupHandler)
	restoreToken := seedWorldViewToken(t, srv, "native-map-restorer", "native-map-restore-role", []rbac.Permission{
		{Resource: rbac.ResourceBackups, Action: rbac.ActionUpdate, ObjectID: "*"},
	})
	archiveRoot := t.TempDir()
	archivedRegion := filepath.Join(archiveRoot, level, "region", "r.0.0.mca")
	if err := os.MkdirAll(filepath.Dir(archivedRegion), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(archiveRoot, level, "level.dat"), levelNBT, 0o644); err != nil {
		t.Fatal(err)
	}
	restoredRegion := bytes.Clone(region)
	binary.BigEndian.PutUint32(restoredRegion[4096:], 1795200100)
	if err := os.WriteFile(archivedRegion, restoredRegion, 0o644); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "world.zip")
	if _, err := files.CreateZipArchive([]string{level}, archiveRoot, archive, true); err != nil {
		t.Fatal(err)
	}
	sum, err := files.SHA256File(archive)
	if err != nil {
		t.Fatal(err)
	}
	record := &storage.BackupRecord{ID: "native-map-backup", ServerID: server.ID, Name: "world.zip", Path: archive, SHA256: sum}
	if err := srv.store.CreateBackupRecord(ctx, record); err != nil {
		t.Fatal(err)
	}
	restore := connect.NewRequest(&v1.RestoreBackupRequest{Id: record.ID})
	restore.Header().Set("Authorization", "Bearer "+restoreToken)
	restored, err := carbonpanelv1connect.NewBackupServiceClient(ts.Client(), ts.URL).RestoreBackup(ctx, restore)
	if err != nil || !restored.Msg.GetSuccess() {
		t.Fatalf("mounted backup restore failed: %v, %v", restored, err)
	}
	asset(granted, "native/manifest.json", http.StatusConflict)
	asset(granted, tileAsset, http.StatusConflict)
	session, err = issue(granted, nil)
	if err != nil || session.GetProxyPath() == "" {
		t.Fatalf("no new session after restoring the same level: %v, %v", session, err)
	}
	_, restoredHeader := asset(granted, tileAsset, http.StatusOK)
	if restoredHeader.Get("X-CarbonPanel-Tile-Saved-At") == header.Get("X-CarbonPanel-Tile-Saved-At") {
		t.Fatal("new map session did not report the restored region timestamp")
	}

	replacement := "new_active_level"
	cfg.Level = &replacement
	if err := srv.store.SaveServerConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	asset(granted, tileAsset, http.StatusConflict)
	observed, err = os.ReadFile(regionPath)
	if err != nil || !bytes.Equal(observed, restoredRegion) {
		t.Fatalf("native viewing changed the restored world fixture: %v", err)
	}
}

func nativeSparseRegion(t *testing.T) []byte {
	t.Helper()
	var raw bytes.Buffer
	tag := func(kind byte, name string) {
		raw.WriteByte(kind)
		_ = binary.Write(&raw, binary.BigEndian, uint16(len(name)))
		raw.WriteString(name)
	}
	tag(10, "")
	for _, value := range []struct {
		name  string
		value int32
	}{{"DataVersion", 3953}, {"xPos", 0}, {"zPos", 0}, {"yPos", -4}} {
		tag(3, value.name)
		_ = binary.Write(&raw, binary.BigEndian, value.value)
	}
	tag(8, "Status")
	_ = binary.Write(&raw, binary.BigEndian, uint16(14))
	raw.WriteString("minecraft:full")
	tag(10, "Heightmaps")
	tag(12, "WORLD_SURFACE")
	_ = binary.Write(&raw, binary.BigEndian, int32(37))
	var heights uint64
	for i := range 7 {
		heights |= 128 << (i * 9)
	}
	for range 37 {
		_ = binary.Write(&raw, binary.BigEndian, heights)
	}
	raw.Write([]byte{0, 0})

	var compressed bytes.Buffer
	zip := zlib.NewWriter(&compressed)
	if _, err := zip.Write(raw.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := zip.Close(); err != nil {
		t.Fatal(err)
	}
	region := make([]byte, 3*4096)
	region[2], region[3] = 2, 1
	binary.BigEndian.PutUint32(region[4096:], 1795200000)
	binary.BigEndian.PutUint32(region[8192:], uint32(compressed.Len()+1))
	region[8196] = 2
	copy(region[8197:], compressed.Bytes())
	return region
}
