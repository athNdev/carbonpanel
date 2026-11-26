// SPDX-License-Identifier: MPL-2.0
package services

import (
	"archive/zip"
	"context"
	"encoding/json"
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

func TestMountedWorldImportRejectsInvalidUploadChecksums(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	svc := newWorldRPCService(t, store, t.TempDir(), &worldTestLifecycle{})
	svc.uploads = upload.NewManager(t.TempDir(), time.Minute, 10<<20, logger.New())
	t.Cleanup(svc.uploads.Stop)
	archive, err := worldlib.WriteFreshWorldArchive(map[string]string{"level-name": "world"}, "")
	if err != nil {
		t.Fatal(err)
	}
	session, err := svc.uploads.InitSession("import.zip", int64(len(archive)), int32(len(archive)))
	if err != nil {
		t.Fatal(err)
	}
	if complete, err := svc.uploads.WriteChunk(session.ID, 0, archive); err != nil || !complete {
		t.Fatalf("upload completed=%t, error=%v", complete, err)
	}
	pattern, handler := carbonpanelv1connect.NewWorldServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(pattern, handler)
	server := httptest.NewServer(mux)
	defer server.Close()
	client := carbonpanelv1connect.NewWorldServiceClient(server.Client(), server.URL)
	for label, checksum := range map[string]string{"missing": "", "malformed": "bad-digest"} {
		t.Run(label, func(t *testing.T) {
			_, err := client.ImportWorld(ctx, connect.NewRequest(&v1.ImportWorldRequest{
				Source: "upload", UploadId: session.ID, LicenseAttestation: "Owned fixture",
				Sha256Checksum: checksum, RequestId: "invalid-checksum-" + label,
			}))
			if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "SHA-256") {
				t.Fatalf("validation = %v, want InvalidArgument for %s checksum", err, label)
			}
		})
	}
	jobs, err := store.ListWorldImportJobs(ctx, "", 10)
	if err != nil || len(jobs) != 0 {
		t.Fatalf("invalid input created import jobs: %+v, %v", jobs, err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = client.ImportWorld(ctx, connect.NewRequest(&v1.ImportWorldRequest{
		Source: "upload", UploadId: session.ID, LicenseAttestation: "Owned fixture",
		Sha256Checksum: sha256Hex(archive), RequestId: "valid-checksum-storage-failure",
	}))
	if connect.CodeOf(err) != connect.CodeInternal {
		t.Fatalf("storage failure = %v, want Internal", err)
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

// TestCheckCompatibilityRequiresExplicitTargetWithoutServerScope proves the
// version-only preview form still requires the caller to name a target. The
// revision's stored MC version is a fallback for reporting, not a substitute
// for the request field, so omission is InvalidArgument rather than accepted.
func TestCheckCompatibilityRequiresExplicitTargetWithoutServerScope(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	dataRoot := filepath.Join(root, "server")
	seedWorldRPC(t, store, dataRoot, libraryRoot, 7)
	svc := newWorldRPCService(t, store, libraryRoot, &worldTestLifecycle{})

	path := filepath.Join(libraryRoot, "named-preview.zip")
	sum := writeLayoutArchive(t, path, map[string]string{"world/level.dat": "nbt"})
	rev := &storage.WorldRevision{
		ID: worldlib.RevisionID(sum), WorldEntryID: "wld_named_preview", SHA256: sum,
		SizeBytes: int64(len(readFileBytes(t, path))), EntryCount: 1,
		MCVersion: "1.21.1", ModLoader: "VANILLA", StoragePath: path,
	}
	if err := store.PutWorldRevision(ctx, rev); err != nil {
		t.Fatal(err)
	}

	_, err := svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: rev.ID,
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("omitted target = %v (%v), want invalid_argument", connect.CodeOf(err), err)
	}
	if _, err := svc.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: rev.ID, TargetMcVersion: "1.21.1",
	})); err != nil {
		t.Fatalf("explicit target preview: %v", err)
	}
}

// TestMountedConnectRefusesFlatWithExistingCompanions exercises the generated
// Connect handler (not direct method calls) against an existing live world and
// companion directory, proving a flat revision is refused without mutation.
func TestMountedConnectRefusesUnsupportedLayoutWithExistingCompanions(t *testing.T) {
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

	life := &worldTestLifecycle{running: true}
	svc := newWorldRPCService(t, store, libraryRoot, life)
	pattern, handler := carbonpanelv1connect.NewWorldServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(pattern, handler)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := carbonpanelv1connect.NewWorldServiceClient(ts.Client(), ts.URL)

	for _, tc := range []struct {
		label, wantMessage string
		entries            map[string]string
	}{
		{"flat", "named world folder", map[string]string{"level.dat": "nbt"}},
		{"unrelated-family", "unrelated world families", map[string]string{
			"world/level.dat": "nbt", "other//level.dat": "other nbt",
		}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			archive := filepath.Join(libraryRoot, tc.label+".zip")
			sum := writeLayoutArchive(t, archive, tc.entries)
			rev := &storage.WorldRevision{
				ID: worldlib.RevisionID(sum), WorldEntryID: "wld_mounted_" + tc.label, SHA256: sum,
				SizeBytes: int64(len(readFileBytes(t, archive))), EntryCount: int32(len(tc.entries)),
				MCVersion: "1.21.1", ModLoader: "VANILLA", StoragePath: archive,
			}
			if err := store.PutWorldRevision(ctx, rev); err != nil {
				t.Fatal(err)
			}
			_, previewErr := client.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
				RevisionId: rev.ID, ServerId: server.ID,
			}))
			if connect.CodeOf(previewErr) != connect.CodeInvalidArgument || !strings.Contains(previewErr.Error(), tc.wantMessage) {
				t.Fatalf("mounted preview = %v, want %q", previewErr, tc.wantMessage)
			}
			_, bindErr := client.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
				ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "mounted-" + tc.label,
			}))
			if connect.CodeOf(bindErr) != connect.CodeInvalidArgument || !strings.Contains(bindErr.Error(), tc.wantMessage) {
				t.Fatalf("mounted bind = %v, want %q", bindErr, tc.wantMessage)
			}
		})
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
	_, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: "world", RevisionId: rev.ID, RequestId: "configured-level",
	}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("bind with differing request level = %v", err)
	}
	out, err := svc.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
		ServerId: server.ID, LevelName: " " + level + " ", RevisionId: rev.ID, RequestId: "configured-level",
	}))
	if err != nil || out.Msg.GetOutcome() != "success" {
		t.Fatalf("bind with configured request level = %+v, %v", out, err)
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

	archivePath := filepath.Join(libraryRoot, "named.zip")
	sum := writeLayoutArchive(t, archivePath, map[string]string{
		"world//level.dat":              "published-world",
		"config/companion.txt":          "archived-root-companion",
		"revision.zip/companion.txt":    "archive-name-companion",
		"world/datapacks/in-family.txt": "archived-in-family",
	})
	rev := &storage.WorldRevision{
		ID: worldlib.RevisionID(sum), WorldEntryID: "wld_named", SHA256: sum,
		EntryCount: 4, MCVersion: "1.21.1", ModLoader: "VANILLA",
		StoragePath: archivePath,
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
	bind := func(level, revision, key, window string) (*connect.Response[v1.BindWorldResponse], error) {
		return client.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
			ServerId: server.ID, LevelName: level, RevisionId: revision, RequestId: key, Window: window,
		}))
	}
	if _, err := bind("another", rev.ID, "mounted-mismatch", ""); connect.CodeOf(err) != connect.CodeInvalidArgument ||
		!strings.Contains(err.Error(), "does not match configured level") {
		t.Fatalf("mounted explicit target mismatch = %v", err)
	}
	if ops, err := store.ListServerOperations(ctx); err != nil || len(ops) != 0 || life.stops != 0 || life.starts != 0 {
		t.Fatalf("fresh refusal changed journal or lifecycle: %+v, %v, %+v", ops, err, life)
	}
	archiveHash := sha256Hex(readFileBytes(t, archivePath))
	const key = "mounted-named-bind"
	out, err := bind("", rev.ID, key, "")
	if err != nil || out.Msg.GetOutcome() != "success" || out.Msg.GetBinding().GetLevelName() != "world" {
		t.Fatalf("mounted named bind = %+v, %v", out, err)
	}
	for _, level := range []string{"", " world "} {
		again, err := bind(level, rev.ID, key, "")
		if err != nil || again.Msg.GetOperationId() != out.Msg.GetOperationId() {
			t.Fatalf("equivalent retry for level %q = %+v, %v", level, again, err)
		}
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
	cfg, err := store.GetServerConfig(ctx, server.ID)
	if err != nil {
		t.Fatal(err)
	}
	custom := "level-name=new-save"
	cfg.CustomServerProperties = &custom
	if err := store.SaveServerConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
		RevisionId: rev.ID, ServerId: server.ID,
	})); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("preview with changed configured level = %v", err)
	}
	if _, err := bind("", rev.ID, "new-bind-wrong-layout", ""); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("fresh bind with changed configured level = %v", err)
	}

	// Simulate completion before the audit event was recorded.
	if err := store.DB().Where("event = ? AND request_id = ?", activity.EventWorldBind, key).
		Delete(&storage.ActivityLog{}).Error; err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct{ level, revision, window string }{
		{"new-save", rev.ID, ""},
		{"world", "different-revision", ""},
		{"world", rev.ID, "next_restart"},
	} {
		if _, err := bind(input.level, input.revision, key, input.window); connect.CodeOf(err) != connect.CodeFailedPrecondition {
			t.Fatalf("changed replay payload %+v = %v", input, err)
		}
	}
	for _, level := range []string{"", " world "} {
		again, err := bind(level, rev.ID, key, "")
		if err != nil || again.Msg.GetOperationId() != out.Msg.GetOperationId() || again.Msg.GetBinding().GetId() != binding.ID {
			t.Fatalf("historical retry for level %q = %+v, %v", level, again, err)
		}
	}
	var event storage.ActivityLog
	if err := store.DB().Where("event = ? AND request_id = ?", activity.EventWorldBind, key).First(&event).Error; err != nil {
		t.Fatal(err)
	}
	var properties map[string]string
	if err := json.Unmarshal([]byte(event.Properties), &properties); err != nil ||
		properties["level_name"] != "world" || properties["revision_id"] != rev.ID ||
		properties["operation_id"] != out.Msg.GetOperationId() {
		t.Fatalf("recovered audit properties = %v, %v", properties, err)
	}
	if got := countWorldEvents(t, store, activity.EventWorldBind, server.ID, key); got != 1 {
		t.Fatalf("recovered audit count = %d", got)
	}
	if ops, err := store.ListServerOperations(ctx); err != nil || len(ops) != 1 || life.stops != 1 || life.starts != 1 {
		t.Fatalf("replays or refusals changed journal or lifecycle: %+v, %v, %+v", ops, err, life)
	}
	if got := sha256Hex(readFileBytes(t, archivePath)); got != archiveHash {
		t.Fatalf("stored revision changed: %s -> %s", archiveHash, got)
	}
	if got := string(readFileBytes(t, filepath.Join(dataRoot, "world", "level.dat"))); got != "published-world" {
		t.Fatalf("original world changed during replay: %q", got)
	}
	for _, rootCompanion := range []string{"new-save", "revision.zip"} {
		if _, err := os.Stat(filepath.Join(dataRoot, rootCompanion)); !os.IsNotExist(err) {
			t.Fatalf("unexpected live directory %q: %v", rootCompanion, err)
		}
	}

}

func TestMountedWorldOperationsDistinguishMissingConfigAndServer(t *testing.T) {
	ctx := context.Background()
	store := newWorldRPCStore(t)
	root := t.TempDir()
	libraryRoot := filepath.Join(root, "library")
	server, revision := seedWorldRPC(t, store, filepath.Join(root, "data"), libraryRoot, 7)
	life := &worldTestLifecycle{}
	svc := newWorldRPCService(t, store, libraryRoot, life)
	pattern, handler := carbonpanelv1connect.NewWorldServiceHandler(svc)
	mux := http.NewServeMux()
	mux.Handle(pattern, handler)
	ts := httptest.NewServer(mux)
	defer ts.Close()
	client := carbonpanelv1connect.NewWorldServiceClient(ts.Client(), ts.URL)
	bind := func(serverID, requestID string) (*connect.Response[v1.BindWorldResponse], error) {
		return client.BindWorld(ctx, connect.NewRequest(&v1.BindWorldRequest{
			ServerId: serverID, RevisionId: revision.ID, RequestId: requestID,
		}))
	}
	first, err := bind(server.ID, "retained-bind")
	if err != nil || first.Msg.GetOutcome() != "success" {
		t.Fatalf("initial bind = %+v, %v", first, err)
	}
	if err := store.DB().Where("server_id = ?", server.ID).Delete(&storage.ServerConfig{}).Error; err != nil {
		t.Fatal(err)
	}

	for _, target := range []struct {
		serverID string
		want     connect.Code
	}{
		{server.ID, connect.CodeFailedPrecondition},
		{"missing-server", connect.CodeNotFound},
	} {
		if _, err := client.CheckCompatibility(ctx, connect.NewRequest(&v1.CheckCompatibilityRequest{
			ServerId: target.serverID, RevisionId: revision.ID,
		})); connect.CodeOf(err) != target.want {
			t.Fatalf("preview for %s = %v, want %v", target.serverID, err, target.want)
		}
		if _, err := bind(target.serverID, "fresh-bind"); connect.CodeOf(err) != target.want {
			t.Fatalf("fresh bind for %s = %v, want %v", target.serverID, err, target.want)
		}
	}
	replay, err := bind(server.ID, "retained-bind")
	if err != nil || replay.Msg.GetOperationId() != first.Msg.GetOperationId() ||
		replay.Msg.GetBinding().GetId() != first.Msg.GetBinding().GetId() {
		t.Fatalf("retained replay without config = %+v, %v", replay, err)
	}
	ops, err := store.ListServerOperations(ctx)
	if err != nil || len(ops) != 1 || life.stops != 0 || life.starts != 0 {
		t.Fatalf("refusal or replay changed lifecycle or journal: %+v, %+v, %v", life, ops, err)
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

func TestListBindingsReportsConfiguredWorldWithoutCreatingLibraryEntry(t *testing.T) {
	ctx := t.Context()
	store := newWorldRPCStore(t)
	server := &storage.Server{ID: "existing", Name: "Running", Status: storage.StatusRunning}
	if err := store.CreateServer(ctx, server); err != nil {
		t.Fatal(err)
	}
	props := "level-name=survival"
	if err := store.SaveServerConfig(ctx, &storage.ServerConfig{ServerID: server.ID, CustomServerProperties: &props}); err != nil {
		t.Fatal(err)
	}
	s := newWorldRPCService(t, store, t.TempDir(), &worldTestLifecycle{})
	for _, tc := range []struct{ id, want string }{{server.ID, "survival"}, {"", ""}, {"unknown", ""}} {
		res, err := s.ListBindings(ctx, connect.NewRequest(&v1.ListBindingsRequest{ServerId: tc.id}))
		if err != nil || res.Msg.ConfiguredLevelName != tc.want || len(res.Msg.Bindings) != 0 {
			t.Fatalf("scope %q: got %v, %v", tc.id, res, err)
		}
	}
	res, err := s.ListWorldEntries(ctx, connect.NewRequest(&v1.ListWorldEntriesRequest{}))
	if err != nil || len(res.Msg.Entries) != 0 {
		t.Fatalf("library changed on observation: %v %v", res, err)
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
	if connect.CodeOf(err) != connect.CodeCanceled {
		t.Fatalf("bind cancellation = %v, want canceled", err)
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
