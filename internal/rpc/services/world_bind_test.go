// SPDX-License-Identifier: MPL-2.0
package services

import (
	"archive/zip"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/athNdev/carbon-panel/internal/activity"
	"github.com/athNdev/carbon-panel/internal/config"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/worldlib"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	"github.com/athNdev/carbon-panel/pkg/upload"
)

type worldTestLifecycle struct {
	running   bool
	stops     int
	starts    int
	failStart bool
}

func (f *worldTestLifecycle) BeginTransition(string) (func(), error) {
	return func() {}, nil
}

func (f *worldTestLifecycle) IsRunning(context.Context, string) (bool, error) { return f.running, nil }
func (f *worldTestLifecycle) StopServer(context.Context, string) error {
	f.stops++
	f.running = false
	return nil
}
func (f *worldTestLifecycle) StartServer(context.Context, string) error {
	f.starts++
	if f.failStart {
		return errors.New("restart not observed")
	}
	f.running = true
	return nil
}

func newWorldRPCStore(t *testing.T) *storage.Store {
	t.Helper()
	store, err := storage.NewSQLiteStore(&config.Config{Database: config.DatabaseConfig{
		Path: filepath.Join(t.TempDir(), "world.db"), AutoMigrate: true,
	}, Docker: config.DockerConfig{Host: "unix:///var/run/docker.sock"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func seedWorldRPC(t *testing.T, store *storage.Store, dataRoot, libraryRoot string, generation int64) (*storage.Server, *storage.WorldRevision) {
	t.Helper()
	ctx := context.Background()
	// A legacy local node id keeps the storage path local, while the Node row
	// supplies a non-default generation so the test proves real DB sourcing.
	node := &storage.Node{ID: "local", Name: "local", IsLocal: true, EndpointGeneration: generation, Enabled: true}
	if err := store.CreateNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	server := &storage.Server{ID: "srv-real", Name: "Real", NodeID: node.ID, DataPath: dataRoot, Status: storage.StatusStopped}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	level := "world"
	if err := store.SaveServerConfig(ctx, &storage.ServerConfig{ID: server.ID, ServerID: server.ID, Level: &level}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dataRoot, "world"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(libraryRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	archive, err := worldlib.WriteFreshWorldArchive(map[string]string{"level-name": "world"}, "")
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(libraryRoot, "rev.zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}
	sha := "4444444444444444444444444444444444444444444444444444444444444444"
	rev := &storage.WorldRevision{
		ID: worldlib.RevisionID(sha), WorldEntryID: "wld_rpc", SHA256: sha,
		SizeBytes: int64(len(archive)), EntryCount: 3, MCVersion: "1.21.4",
		ModLoader: "VANILLA", StoragePath: archivePath,
	}
	if err := store.PutWorldRevision(ctx, rev); err != nil {
		t.Fatal(err)
	}
	return server, rev
}

func newWorldRPCService(t *testing.T, store *storage.Store, libraryRoot string, life *worldTestLifecycle) *WorldService {
	t.Helper()
	svc := NewWorldService(store, &config.Config{}, logger.New(), nodestorage.NewResolver(nil), nil, libraryRoot)
	svc.SetBinder(worldlib.NewBinder(store, logger.New(), svc.resolver, nil, life, libraryRoot))
	return svc
}

func TestImportedWorldCanBePreviewedAndBound(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	dataRoot := filepath.Join(root, "server")
	server, _ := seedWorldRPC(t, store, dataRoot, libraryRoot, 7)
	server.MCVersion = "1.21.1"
	server.ModLoader = storage.ModLoaderVanilla
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	archive, err := worldlib.WriteFreshWorldArchive(map[string]string{"level-name": "world"}, "3953")
	if err != nil {
		t.Fatal(err)
	}
	manager := upload.NewManager(t.TempDir(), time.Minute, 10<<20, logger.New())
	t.Cleanup(manager.Stop)
	session, err := manager.InitSession("import.zip", int64(len(archive)), int32(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if complete, err := manager.WriteChunk(session.ID, 0, archive); err != nil || !complete {
		t.Fatalf("upload completed=%t, error=%v", complete, err)
	}
	life := &worldTestLifecycle{running: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)
	svc.uploads = manager
	imported, err := svc.ImportWorld(ctx, connect.NewRequest(&v1.ImportWorldRequest{
		Source: "upload", UploadId: session.ID, DisplayName: "Imported world",
		LicenseAttestation: "Test archive is owned by this fixture", Sha256Checksum: sha256Hex(archive),
		RequestId: "rpc-import-then-bind",
	}))
	if err != nil {
		t.Fatal(err)
	}
	revisionID := imported.Msg.GetRevision().GetId()
	preview, err := svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: revisionID, ServerId: server.ID,
	}))
	if err != nil {
		t.Fatal(err)
	}
	for _, finding := range preview.Msg.GetReport().GetFindings() {
		if strings.Contains(finding.GetReason(), "Archive could not be examined") {
			t.Fatalf("preview did not open the imported archive: %s", finding.GetReason())
		}
	}
	out, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: revisionID, RequestId: "rpc-bind-imported",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Msg.GetOutcome() != "success" || out.Msg.GetBinding().GetRevisionId() != revisionID ||
		life.stops != 1 || life.starts != 1 {
		t.Fatalf("imported world was not bound and restarted: %v, lifecycle=%+v", out.Msg, life)
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "world", "level.dat")); err != nil {
		t.Fatal(err)
	}
}

func writeLayoutArchive(t *testing.T, path string, entries map[string]string) string {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, data := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256Hex(raw)
}

// TestWorldLayoutRefusalsAreSideEffectFreeWithExistingLiveWorld exercises the
// mounted service against an existing live marker and pre-gate stored revisions
// for every unsupported layout, then proves a documented named multi-family
// backup with companions still previews and binds (OSS-432).
func TestWorldLayoutRefusalsAreSideEffectFreeWithExistingLiveWorld(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	dataRoot := filepath.Join(root, "server")
	server, _ := seedWorldRPC(t, store, dataRoot, libraryRoot, 7)
	server.MCVersion = "1.21.1"
	server.ModLoader = storage.ModLoaderVanilla
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	live := filepath.Join(dataRoot, "world", "level.dat")
	if err := os.MkdirAll(filepath.Dir(live), 0o755); err != nil {
		t.Fatal(err)
	}
	const existingBytes = "existing-live-world"
	if err := os.WriteFile(live, []byte(existingBytes), 0o644); err != nil {
		t.Fatal(err)
	}
	liveHash := sha256Hex(readFileBytes(t, live))

	life := &worldTestLifecycle{running: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)
	storeRevision := func(label string, entries map[string]string) (*storage.WorldRevision, string) {
		path := filepath.Join(libraryRoot, label+".zip")
		sum := writeLayoutArchive(t, path, entries)
		rev := &storage.WorldRevision{
			ID: worldlib.RevisionID(sum), WorldEntryID: "wld_" + label, SHA256: sum,
			SizeBytes: int64(len(readFileBytes(t, path))), EntryCount: int32(len(entries)),
			MCVersion: "1.21.1", ModLoader: "VANILLA", StoragePath: path,
		}
		if err := store.PutWorldRevision(ctx, rev); err != nil {
			t.Fatal(err)
		}
		return rev, sum
	}
	assertUntouched := func() {
		t.Helper()
		if life.stops != 0 || life.starts != 0 || !life.running {
			t.Fatalf("refusal mutated lifecycle: %+v", life)
		}
		if _, err := store.GetWorldBinding(ctx, server.ID, "world"); err == nil {
			t.Fatal("refusal created a binding")
		}
		if got := sha256Hex(readFileBytes(t, live)); got != liveHash {
			t.Fatalf("refusal changed live world bytes: %s -> %s", liveHash, got)
		}
	}

	for _, tc := range []struct {
		label, wantMsg string
		entries        map[string]string
	}{
		{"flat", "named world folder", map[string]string{"level.dat": "nbt"}},
		{"mixed", "ambiguous archive", map[string]string{"level.dat": "nbt", "world/level.dat": "nbt"}},
		{"nested", "no usable level family", map[string]string{"server/world/level.dat": "nbt"}},
		{"casevariant", "no usable level family", map[string]string{"WORLD/LEVEL.DAT": "nbt"}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			rev, sum := storeRevision("refusal-"+tc.label, tc.entries)
			beforeOps, err := store.ListServerOperations(ctx)
			if err != nil {
				t.Fatal(err)
			}
			beforeEvents := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "")
			_, pErr := svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
				RevisionId: rev.ID, ServerId: server.ID,
			}))
			if pErr == nil || !strings.Contains(pErr.Error(), tc.wantMsg) {
				t.Fatalf("preview = %v, want %q", pErr, tc.wantMsg)
			}
			_, bErr := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
				ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "refuse-" + tc.label,
			}))
			if bErr == nil || !strings.Contains(bErr.Error(), tc.wantMsg) {
				t.Fatalf("bind = %v, want %q", bErr, tc.wantMsg)
			}
			afterOps, err := store.ListServerOperations(ctx)
			if err != nil || len(afterOps) != len(beforeOps) {
				t.Fatalf("journal rows %d -> %d (err=%v)", len(beforeOps), len(afterOps), err)
			}
			if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, ""); got != beforeEvents {
				t.Fatalf("bind events %d -> %d", beforeEvents, got)
			}
			assertUntouched()
			// Immutable revision bytes are unchanged (the gate only reads).
			if got := sha256Hex(readFileBytes(t, rev.StoragePath)); got != sum {
				t.Fatalf("revision bytes changed: %s -> %s", sum, got)
			}
		})
	}

	t.Run("target mismatch", func(t *testing.T) {
		rev, sum := storeRevision("mismatch", map[string]string{"MySave/level.dat": "nbt"})
		_, pErr := svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
			RevisionId: rev.ID, ServerId: server.ID,
		}))
		if pErr == nil || !strings.Contains(pErr.Error(), `family "MySave" does not match target level "world"`) {
			t.Fatalf("preview mismatch = %v", pErr)
		}
		_, bErr := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
			ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "refuse-mismatch",
		}))
		if bErr == nil || !strings.Contains(bErr.Error(), `family "MySave" does not match target level "world"`) {
			t.Fatalf("bind mismatch = %v", bErr)
		}
		assertUntouched()
		if got := sha256Hex(readFileBytes(t, rev.StoragePath)); got != sum {
			t.Fatalf("revision bytes changed: %s -> %s", sum, got)
		}
	})

	t.Run("named multi-family backup with companions", func(t *testing.T) {
		rev, _ := storeRevision("backup", map[string]string{
			"world/level.dat": "new-world", "world/DIM-1/region/r.0.0.mca": "nether-data",
			"world_nether/level.dat": "nether", "server.properties": "level-name=world",
			"world/config/companion.txt": "archived-companion-bytes",
		})
		if _, err := svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
			RevisionId: rev.ID, ServerId: server.ID,
		})); err != nil {
			t.Fatalf("preview valid backup: %v", err)
		}
		out, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
			ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "bind-backup",
		}))
		if err != nil || out.Msg.GetOutcome() != "success" {
			t.Fatalf("bind valid backup = %+v, %v", out, err)
		}
		if got := string(readFileBytes(t, live)); got != "new-world" {
			t.Fatalf("live world bytes = %q, want published archive bytes", got)
		}
		if got := string(readFileBytes(t, filepath.Join(dataRoot, "world", "DIM-1", "region", "r.0.0.mca"))); got != "nether-data" {
			t.Fatalf("in-family dimension data was not preserved: %q", got)
		}
		if _, err := os.Stat(filepath.Join(dataRoot, "world_nether", "level.dat")); err != nil {
			t.Fatalf("named dimension sibling was not published: %v", err)
		}
		if got := string(readFileBytes(t, filepath.Join(dataRoot, "world", "config", "companion.txt"))); got != "archived-companion-bytes" {
			t.Fatalf("archived backup companion was not preserved: %q", got)
		}
	})
}

// TestMountedConnectRefusesFlatWithExistingCompanions exercises the generated
// Connect handler (not direct method calls) against an existing live world and
// companion directory, proving a flat revision is refused without mutation.
func TestMountedConnectRefusesFlatWithExistingCompanions(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	dataRoot := filepath.Join(root, "server")
	server, _ := seedWorldRPC(t, store, dataRoot, libraryRoot, 7)

	live := filepath.Join(dataRoot, "world", "level.dat")
	if err := os.WriteFile(live, []byte("existing-live"), 0o644); err != nil {
		t.Fatal(err)
	}
	companion := filepath.Join(dataRoot, "world", "config", "companion.txt")
	if err := os.MkdirAll(filepath.Dir(companion), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(companion, []byte("existing-companion"), 0o644); err != nil {
		t.Fatal(err)
	}
	liveHash := sha256Hex(readFileBytes(t, live))
	companionHash := sha256Hex(readFileBytes(t, companion))

	sum := writeLayoutArchive(t, filepath.Join(libraryRoot, "flat.zip"), map[string]string{"level.dat": "nbt"})
	rev := &storage.WorldRevision{
		ID: worldlib.RevisionID(sum), WorldEntryID: "wld_mounted_flat", SHA256: sum,
		SizeBytes: int64(len(sum)), EntryCount: 1, MCVersion: "1.21.1", ModLoader: "VANILLA",
		StoragePath: filepath.Join(libraryRoot, "flat.zip"),
	}
	if err := store.PutWorldRevision(ctx, rev); err != nil {
		t.Fatal(err)
	}

	life := &worldTestLifecycle{running: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)
	pattern, handler := carbonpanelv1connect.NewWorldServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(pattern, handler)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := carbonpanelv1connect.NewWorldServiceClient(ts.Client(), ts.URL)

	_, pErr := client.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: rev.ID, ServerId: server.ID,
	}))
	if pErr == nil || !strings.Contains(pErr.Error(), "named world folder") {
		t.Fatalf("mounted preview = %v", pErr)
	}
	_, bErr := client.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "mounted-flat",
	}))
	if bErr == nil || !strings.Contains(bErr.Error(), "named world folder") {
		t.Fatalf("mounted bind = %v", bErr)
	}
	if life.stops != 0 || life.starts != 0 || !life.running {
		t.Fatalf("mounted refusal mutated lifecycle: %+v", life)
	}
	if _, err := store.GetWorldBinding(ctx, server.ID, "world"); err == nil {
		t.Fatal("mounted refusal created a binding")
	}
	if got := sha256Hex(readFileBytes(t, live)); got != liveHash {
		t.Fatalf("mounted refusal changed live marker: %s -> %s", liveHash, got)
	}
	if got := sha256Hex(readFileBytes(t, companion)); got != companionHash {
		t.Fatalf("mounted refusal changed companion: %s -> %s", companionHash, got)
	}
}

// TestConfiguredLevelIsAuthoritativeForPreviewAndBind proves preview and bind
// use the same configured non-default level; a differing caller level cannot
// retarget new work.
func TestConfiguredLevelIsAuthoritativeForPreviewAndBind(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	dataRoot := filepath.Join(root, "server")
	server, _ := seedWorldRPC(t, store, dataRoot, libraryRoot, 7)
	const level = "custom_world"
	if err := store.SaveServerConfig(ctx, &storage.ServerConfig{
		ID: server.ID, ServerID: server.ID, Level: strPtrFixture(level),
	}); err != nil {
		t.Fatal(err)
	}
	sum := writeLayoutArchive(t, filepath.Join(libraryRoot, "custom.zip"), map[string]string{
		level + "/level.dat": "custom-world",
	})
	rev := &storage.WorldRevision{
		ID: worldlib.RevisionID(sum), WorldEntryID: "wld_custom", SHA256: sum,
		SizeBytes: int64(len(sum)), EntryCount: 1, MCVersion: "1.21.1", ModLoader: "VANILLA",
		StoragePath: filepath.Join(libraryRoot, "custom.zip"),
	}
	if err := store.PutWorldRevision(ctx, rev); err != nil {
		t.Fatal(err)
	}
	life := &worldTestLifecycle{}
	svc := newWorldRPCService(t, store, libraryRoot, life)
	if _, err := svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: rev.ID, ServerId: server.ID,
	})); err != nil {
		t.Fatalf("preview configured level: %v", err)
	}
	// A stale caller level is not allowed to retarget new work; the canonical
	// configured level is used for storage and the audit event.
	differing, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "configured-level",
	}))
	if err != nil || differing.Msg.GetOutcome() != "success" {
		t.Fatalf("bind with differing request level = %+v, %v", differing, err)
	}
	binding, err := store.GetWorldBinding(ctx, server.ID, level)
	if err != nil || binding.LevelName != level {
		t.Fatalf("canonical binding = %+v, %v", binding, err)
	}
	if got := string(readFileBytes(t, filepath.Join(dataRoot, level, "level.dat"))); got != "custom-world" {
		t.Fatalf("configured level was not published: %q", got)
	}
}

// TestVersionOnlyPreviewStillWorks proves the documented server-or-version
// preview form applies the archive layout policy without requiring a server.
func TestVersionOnlyPreviewStillWorks(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	dataRoot := filepath.Join(root, "server")
	seedWorldRPC(t, store, dataRoot, libraryRoot, 7)
	life := &worldTestLifecycle{running: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)

	validSum := writeLayoutArchive(t, filepath.Join(libraryRoot, "version-valid.zip"), map[string]string{
		"world/level.dat": "nbt",
	})
	valid := &storage.WorldRevision{
		ID: worldlib.RevisionID(validSum), WorldEntryID: "wld_version_valid", SHA256: validSum,
		EntryCount: 1, MCVersion: "1.21.1", ModLoader: "VANILLA",
		StoragePath: filepath.Join(libraryRoot, "version-valid.zip"),
	}
	if err := store.PutWorldRevision(ctx, valid); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: valid.ID, TargetMcVersion: "1.21.1",
	})); err != nil {
		t.Fatalf("version-only preview: %v", err)
	}

	flatSum := writeLayoutArchive(t, filepath.Join(libraryRoot, "version-flat.zip"), map[string]string{
		"level.dat": "nbt",
	})
	flat := &storage.WorldRevision{
		ID: worldlib.RevisionID(flatSum), WorldEntryID: "wld_version_flat", SHA256: flatSum,
		EntryCount: 1, MCVersion: "1.21.1", ModLoader: "VANILLA",
		StoragePath: filepath.Join(libraryRoot, "version-flat.zip"),
	}
	if err := store.PutWorldRevision(ctx, flat); err != nil {
		t.Fatal(err)
	}
	_, err := svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: flat.ID, TargetMcVersion: "1.21.1",
	}))
	if err == nil || !strings.Contains(err.Error(), "named world folder") {
		t.Fatalf("version-only flat preview = %v", err)
	}
}

// TestMountedConnectBindsNamedBackupWithRootCompanions is the mounted positive
// counterpart: a real archive family plus an ordinary top-level config/ folder
// binds without touching a pre-existing live companion, and the audit records
// the canonical configured level.
func TestMountedConnectBindsNamedBackupWithRootCompanions(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	dataRoot := filepath.Join(root, "server")
	server, _ := seedWorldRPC(t, store, dataRoot, libraryRoot, 7)

	liveCompanion := filepath.Join(dataRoot, "config", "companion.txt")
	if err := os.MkdirAll(filepath.Dir(liveCompanion), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(liveCompanion, []byte("live-root-companion"), 0o644); err != nil {
		t.Fatal(err)
	}
	liveHash := sha256Hex(readFileBytes(t, liveCompanion))

	sum := writeLayoutArchive(t, filepath.Join(libraryRoot, "named.zip"), map[string]string{
		"world/level.dat":               "published-world",
		"config/companion.txt":          "archived-root-companion",
		"world/datapacks/in-family.txt": "archived-in-family",
	})
	rev := &storage.WorldRevision{
		ID: worldlib.RevisionID(sum), WorldEntryID: "wld_named", SHA256: sum,
		EntryCount: 3, MCVersion: "1.21.1", ModLoader: "VANILLA",
		StoragePath: filepath.Join(libraryRoot, "named.zip"),
	}
	if err := store.PutWorldRevision(ctx, rev); err != nil {
		t.Fatal(err)
	}
	life := &worldTestLifecycle{}
	svc := newWorldRPCService(t, store, libraryRoot, life)
	pattern, handler := carbonpanelv1connect.NewWorldServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(pattern, handler)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := carbonpanelv1connect.NewWorldServiceClient(ts.Client(), ts.URL)

	if _, err := client.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: rev.ID, TargetMcVersion: "1.21.1",
	})); err != nil {
		t.Fatalf("mounted version-only preview: %v", err)
	}
	if _, err := client.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: rev.ID, ServerId: server.ID,
	})); err != nil {
		t.Fatalf("mounted server preview: %v", err)
	}
	out, err := client.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: " world ", RevisionId: rev.ID, RequestId: "mounted-named-bind",
	}))
	if err != nil || out.Msg.GetOutcome() != "success" {
		t.Fatalf("mounted named bind = %+v, %v", out, err)
	}
	if got := string(readFileBytes(t, filepath.Join(dataRoot, "world", "level.dat"))); got != "published-world" {
		t.Fatalf("published world = %q", got)
	}
	if got := string(readFileBytes(t, filepath.Join(dataRoot, "world", "datapacks", "in-family.txt"))); got != "archived-in-family" {
		t.Fatalf("in-family companion = %q", got)
	}
	if got := sha256Hex(readFileBytes(t, liveCompanion)); got != liveHash {
		t.Fatalf("live root companion changed: %s -> %s", liveHash, got)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "mounted-named-bind"); got != 1 {
		t.Fatalf("bind events = %d", got)
	}
	binding, err := store.GetWorldBinding(ctx, server.ID, "world")
	if err != nil || binding.LevelName != "world" {
		t.Fatalf("binding canonical level = %+v, %v", binding, err)
	}
}

func readFileBytes(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestStoredFlatRevisionPreviewAndBindRefuseWithoutMutation covers a revision
// stored before the import gate existed: preview and bind must agree, and bind
// must not stop the server or write live-world data (OSS-432).
func TestStoredFlatRevisionPreviewAndBindRefuseWithoutMutation(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	dataRoot := filepath.Join(root, "server")
	server, _ := seedWorldRPC(t, store, dataRoot, libraryRoot, 7)
	server.MCVersion = "1.21.1"
	server.ModLoader = storage.ModLoaderVanilla
	if err := store.UpdateServer(ctx, server); err != nil {
		t.Fatal(err)
	}

	flatPath := filepath.Join(libraryRoot, "stored-flat.zip")
	f, err := os.Create(flatPath)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	entry, err := zw.Create("level.dat")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := entry.Write([]byte("nbt")); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(flatPath)
	if err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("f", 64)
	flat := &storage.WorldRevision{
		ID: worldlib.RevisionID(sha), WorldEntryID: "wld_stored_flat", SHA256: sha,
		SizeBytes: info.Size(), EntryCount: 1, MCVersion: "1.21.1", ModLoader: "VANILLA",
		StoragePath: flatPath,
	}
	if err := store.PutWorldRevision(ctx, flat); err != nil {
		t.Fatal(err)
	}

	life := &worldTestLifecycle{running: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)
	assertActionable := func(label string, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "named world folder") {
			t.Fatalf("%s error = %v, want actionable named-world-folder message", label, err)
		}
	}
	_, err = svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: flat.ID, ServerId: server.ID,
	}))
	assertActionable("preview", err)
	_, err = svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: flat.ID, RequestId: "stored-flat-bind",
	}))
	assertActionable("bind", err)

	if life.stops != 0 || life.starts != 0 || !life.running {
		t.Fatalf("refused bind mutated lifecycle: %+v", life)
	}
	if _, err := store.GetWorldBinding(ctx, server.ID, "world"); err == nil {
		t.Fatal("refused bind created a world binding")
	}
	if _, err := os.Stat(filepath.Join(dataRoot, "world", "level.dat")); !os.IsNotExist(err) {
		t.Fatalf("refused bind wrote live world data: %v", err)
	}
}

// TestBindWorldUsesRealNodeGeneration proves a bind succeeds end to end at the
// RPC seam: the generation comes from the Node row, not a context stub.
func TestBindWorldUsesRealNodeGeneration(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	life := &worldTestLifecycle{}
	svc := newWorldRPCService(t, store, libraryRoot, life)

	out, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if out.Msg.Outcome != "success" || out.Msg.Binding == nil {
		t.Fatalf("outcome = %q, binding = %+v", out.Msg.Outcome, out.Msg.Binding)
	}
	if out.Msg.Binding.Generation != 7 {
		t.Fatalf("binding generation = %d, want 7 from the Node row", out.Msg.Binding.Generation)
	}
	binding, err := store.GetWorldBinding(ctx, server.ID, "world")
	if err != nil || binding.Generation != 7 {
		t.Fatalf("stored binding = %+v, %v", binding, err)
	}
}

func TestBindWorldReportsUnknownOutcomeInsteadOfRetryableError(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	life := &worldTestLifecycle{running: true, failStart: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)

	out, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-unknown",
	}))
	if err != nil {
		t.Fatalf("an uncertain outcome must be reported, not returned as a retryable error: %v", err)
	}
	if out.Msg.Outcome != "unknown" {
		t.Fatalf("outcome = %q, want unknown", out.Msg.Outcome)
	}
	if out.Msg.OperationId == "" {
		t.Fatal("unknown outcome must carry the operation id for resolution")
	}
	active, err := store.ListActiveServerOperations(ctx)
	if err != nil || len(active) != 1 || active[0].ID != out.Msg.OperationId {
		t.Fatalf("active = %+v, %v; journal must keep its lock", active, err)
	}
}

func countWorldEvents(t *testing.T, store *storage.Store, event, serverID, requestID string) int64 {
	t.Helper()
	var count int64
	q := store.DB().Model(&storage.ActivityLog{}).Where("event = ? AND subject_id = ?", event, serverID)
	if requestID != "" {
		q = q.Where("request_id = ?", requestID)
	}
	if err := q.Count(&count).Error; err != nil {
		t.Fatalf("count events: %v", err)
	}
	return count
}

func TestBindWorldEmitsExactlyOneEventOnSuccessAndReplay(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	req := connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-event-1",
	})
	if _, err := svc.BindWorld(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-event-1"); got != 1 {
		t.Fatalf("events after first bind = %d, want 1", got)
	}
	// A replay of the same completed request must not emit a duplicate event.
	if _, err := svc.BindWorld(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-event-1"); got != 1 {
		t.Fatalf("events after replay = %d, want 1", got)
	}
}

func TestBindWorldReplayEmitsMissingEventAfterCrashRecovery(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	req := connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-recover-1",
	})
	if _, err := svc.BindWorld(ctx, req); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash after journal completion but before the activity event
	// was durably recorded. A client retry of the same request must recover the
	// missing event exactly once.
	if err := store.DB().Where("event = ? AND request_id = ?", activity.EventWorldBind, "rpc-bind-recover-1").
		Delete(&storage.ActivityLog{}).Error; err != nil {
		t.Fatalf("delete event: %v", err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-recover-1"); got != 0 {
		t.Fatalf("events after simulated crash = %d, want 0", got)
	}
	if _, err := svc.BindWorld(ctx, req); err != nil {
		t.Fatal(err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-recover-1"); got != 1 {
		t.Fatalf("events after recovery replay = %d, want 1", got)
	}
}

func TestBindWorldCancellationBeforeMutationEmitsNoEvent(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	_, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-cancel-1",
	}))
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-cancel-1"); got != 0 {
		t.Fatalf("events after cancelled bind = %d, want 0", got)
	}
}

func TestBindWorldUncertainOutcomeEmitsNoSuccessEvent(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	life := &worldTestLifecycle{running: true, failStart: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)

	out, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-unknown-event",
	}))
	if err != nil {
		t.Fatalf("an uncertain outcome must be reported, not returned as a retryable error: %v", err)
	}
	if out.Msg.Outcome != "unknown" {
		t.Fatalf("outcome = %q, want unknown", out.Msg.Outcome)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-unknown-event"); got != 0 {
		t.Fatalf("success events after uncertain outcome = %d, want 0", got)
	}
	// Keep the existing uncertain-outcome contract: the journal retains its lock.
	active, err := store.ListActiveServerOperations(ctx)
	if err != nil || len(active) != 1 || active[0].ID != out.Msg.OperationId {
		t.Fatalf("active = %+v, %v; journal must keep its lock", active, err)
	}
}

func TestUnbindWorldEmitsExactlyOneEventOnSuccess(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	if _, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-before-unbind",
	})); err != nil {
		t.Fatal(err)
	}
	out, err := svc.UnbindWorld(ctx, connect.NewRequest(&v1.UnbindWorldRequest{
		ServerId: server.ID, LevelName: "world", RequestId: "rpc-unbind-event-1",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if !out.Msg.Removed {
		t.Fatal("expected binding to be removed")
	}
	if got := countWorldEvents(t, store, activity.EventWorldUnbind, server.ID, "rpc-unbind-event-1"); got != 1 {
		t.Fatalf("events after unbind = %d, want 1", got)
	}
}

func TestUnbindWorldReplayEmitsExactlyOneEventWhenBindingAbsent(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	if _, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-before-unbind-replay",
	})); err != nil {
		t.Fatal(err)
	}
	req := connect.NewRequest(&v1.UnbindWorldRequest{
		ServerId: server.ID, LevelName: "world", RequestId: "rpc-unbind-replay-1",
	})
	if _, err := svc.UnbindWorld(ctx, req); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetWorldBinding(ctx, server.ID, "world"); err == nil {
		t.Fatal("expected binding to be absent after unbind")
	}
	if _, err := svc.UnbindWorld(ctx, req); err != nil {
		t.Fatalf("replay with absent binding must be idempotent: %v", err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldUnbind, server.ID, "rpc-unbind-replay-1"); got != 1 {
		t.Fatalf("events after replay with absent binding = %d, want 1", got)
	}
}

// faultyRPCAccessor fails the second family promotion at the real node
// accessor boundary so a failed World Library bind can be exercised through
// the real RPC service without touching host data.
type faultyRPCAccessor struct {
	nodestorage.Accessor
}

func (f *faultyRPCAccessor) Rename(oldRel, newRel string) error {
	if newRel == "world_nether" && strings.Contains(oldRel, "stage-") {
		return errors.New("injected promotion fault")
	}
	return f.Accessor.Rename(oldRel, newRel)
}

func TestBindWorldMultiFamilyRollbackPreservesPriorBindingAndEmitsNoEvent(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, rev := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	families := []string{"world", "world_nether"}
	prior := &storage.WorldBinding{
		ID: "prior-binding", ServerID: server.ID, LevelName: "world", RevisionID: "prior-revision",
		NodeID: server.NodeID, Generation: 7,
	}
	if err := store.CreateWorldBinding(ctx, prior); err != nil {
		t.Fatal(err)
	}
	archive, err := os.Create(rev.StoragePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = archive.Close() })
	staged := zip.NewWriter(archive)
	for _, family := range families {
		if err := os.MkdirAll(filepath.Join(server.DataPath, family, "region"), 0o755); err != nil {
			t.Fatal(err)
		}
		for _, file := range []string{"level.dat", "region/r.0.0.mca"} {
			if err := os.WriteFile(filepath.Join(server.DataPath, family, file), []byte("old-"+family+"/"+file), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		entry, err := staged.Create(family + "/level.dat")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte("new-" + family)); err != nil {
			t.Fatal(err)
		}
	}
	if err := staged.Close(); err != nil {
		t.Fatal(err)
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})
	svc.binder.SetAccessorDecorator(func(acc nodestorage.Accessor) nodestorage.Accessor {
		return &faultyRPCAccessor{Accessor: acc}
	})
	req := connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "rpc-bind-failed-rollback",
	})
	if _, err := svc.BindWorld(ctx, req); err == nil {
		t.Fatal("expected the injected promotion failure")
	}
	for _, family := range families {
		for _, file := range []string{"level.dat", "region/r.0.0.mca"} {
			got, err := os.ReadFile(filepath.Join(server.DataPath, family, file))
			if err != nil || string(got) != "old-"+family+"/"+file {
				t.Fatalf("rollback %s/%s = %q, %v", family, file, got, err)
			}
		}
	}
	binding, err := store.GetWorldBinding(ctx, server.ID, "world")
	if err != nil || binding.ID != prior.ID || binding.RevisionID != prior.RevisionID {
		t.Fatalf("binding after rollback = %+v, %v; want prior", binding, err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-failed-rollback"); got != 0 {
		t.Fatalf("events after failed bind = %d, want 0", got)
	}
	if _, err := svc.BindWorld(ctx, req); err == nil {
		t.Fatal("replay of a failed request must not succeed")
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, "rpc-bind-failed-rollback"); got != 0 {
		t.Fatalf("events after failed replay = %d, want 0", got)
	}
}
