// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/stretchr/testify/require"
)

func fabricOptimizationFixture(t *testing.T, handler http.HandlerFunc, targetName string) (string, modrinthVersionFile, *http.Client) {
	t.Helper()
	mods := t.TempDir()
	existing := filepath.Join(mods, "lithium-1.0.jar")
	require.NoError(t, os.WriteFile(existing, []byte("installed"), 0o600))
	fileServer := httptest.NewServer(handler)
	t.Cleanup(fileServer.Close)
	client := indexers.ProtectCredentialsOnRedirect(&http.Client{
		Timeout: 2 * time.Minute,
		Transport: hostMapTransport{
			base:  http.DefaultTransport,
			hosts: map[string]string{"cdn.modrinth.test": fileServer.URL},
		},
	})
	return existing, modrinthVersionFile{URL: "https://cdn.modrinth.test/" + targetName, Filename: targetName}, client
}

func TestFabricOptimizationInstallPreservesJarOnNon2xx(t *testing.T) {
	existing, target, client := fabricOptimizationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "provider unavailable", http.StatusBadGateway)
	}, "lithium-2.0.jar")

	err := installFabricOptimizationJarWithClient(context.Background(), filepath.Dir(existing), target, "lithium", client)
	require.Error(t, err)
	body, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body), "a non-2xx response must not replace the installed jar")
	require.NoFileExists(t, filepath.Join(filepath.Dir(existing), "lithium-2.0.jar"))
	assertNoFabricOptimizationTemp(t, filepath.Dir(existing))
}

// A 206 response is a declared fragment, not the complete jar, even when its
// Content-Length matches the bytes it sends. It must not replace the installed
// jar.
func TestFabricOptimizationInstallRejectsPartialContent(t *testing.T) {
	existing, target, client := fabricOptimizationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		body := []byte("fragment")
		w.Header().Set("Content-Length", "8")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write(body)
	}, "lithium-2.0.jar")

	err := installFabricOptimizationJarWithClient(context.Background(), filepath.Dir(existing), target, "lithium", client)
	require.Error(t, err)
	body, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body), "a partial response must not replace the installed jar")
	require.NoFileExists(t, filepath.Join(filepath.Dir(existing), "lithium-2.0.jar"))
	assertNoFabricOptimizationTemp(t, filepath.Dir(existing))
}

func TestFabricOptimizationInstallPreservesJarOnTruncatedBody(t *testing.T) {
	existing, target, client := fabricOptimizationFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1024")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("partial"))
	}, "lithium-2.0.jar")

	err := installFabricOptimizationJarWithClient(context.Background(), filepath.Dir(existing), target, "lithium", client)
	require.Error(t, err)
	body, err := os.ReadFile(existing)
	require.NoError(t, err)
	require.Equal(t, "installed", string(body), "a truncated response must not replace the installed jar")
	require.NoFileExists(t, filepath.Join(filepath.Dir(existing), "lithium-2.0.jar"))
	assertNoFabricOptimizationTemp(t, filepath.Dir(existing))
}

func TestFabricOptimizationInstallRefusesLiteralIPBeforeWrite(t *testing.T) {
	var requested bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requested = true
		_, _ = w.Write([]byte("jar"))
	}))
	defer server.Close()
	mods := t.TempDir()
	existing := filepath.Join(mods, "lithium-1.0.jar")
	require.NoError(t, os.WriteFile(existing, []byte("installed"), 0o600))

	target := modrinthVersionFile{URL: server.URL + "/lithium-2.0.jar", Filename: "lithium-2.0.jar"}
	err := installFabricOptimizationJarWithClient(context.Background(), mods, target, "lithium", nil)
	require.Error(t, err)
	require.False(t, requested, "a literal-IP provider target must be refused before any request or write")
	body, readErr := os.ReadFile(existing)
	require.NoError(t, readErr)
	require.Equal(t, "installed", string(body))
	require.NoFileExists(t, filepath.Join(mods, "lithium-2.0.jar"))
	assertNoFabricOptimizationTemp(t, mods)
}

// TestFabricOptimizationClientStripsCredentialsOnCrossOriginRedirect pins the
// production client wrapper used by optimization downloads. The transport is
// the real SSRF-guarded one; the test-only loopback opt-in lets httptest
// fixtures stand in for provider origins.
func TestFabricOptimizationClientStripsCredentialsOnCrossOriginRedirect(t *testing.T) {
	t.Setenv("DISCO_ALLOW_LOOPBACK_TEST", "1")
	redirectedAuthorization := make(chan string, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuthorization <- r.Header.Get("Authorization")
		_, _ = w.Write([]byte("ok"))
	}))
	defer destination.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL+"/lithium-2.0.jar", http.StatusFound)
	}))
	defer origin.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, origin.URL+"/lithium-2.0.jar", nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer must-not-cross-origin")
	resp, err := fabricOptimizationHTTPClient().Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Empty(t, <-redirectedAuthorization, "credentials must be stripped when the origin port changes")
}

func assertNoFabricOptimizationTemp(t *testing.T, modsDir string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(modsDir, ".fabric-optimization-*.tmp"))
	require.NoError(t, err)
	require.Empty(t, matches, "a failed download must not leave a partial temporary jar")
}

type blockingOptimizationFetcher struct {
	mu      sync.Mutex
	calls   int
	started chan string
	release chan struct{}
}

func (f *blockingOptimizationFetcher) Fetch(ctx context.Context, slug, _ string) (*modrinthVersionItem, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()
	select {
	case f.started <- slug:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-f.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &modrinthVersionItem{
		ID: slug, VersionNumber: "1.0.0",
		Files: []modrinthVersionFile{{URL: "https://cdn.modrinth.test/" + slug + ".jar", Filename: slug + ".jar"}},
	}, nil
}

func newFabricOptimizationTestService(t *testing.T) (*ModService, string, *blockingOptimizationFetcher) {
	t.Helper()
	store, server, _ := newContentStageFixture(t, "fabric-optimization-concurrency", storage.StatusStopped)
	jarFile := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("jar"))
	}))
	t.Cleanup(jarFile.Close)
	client := indexers.ProtectCredentialsOnRedirect(&http.Client{
		Timeout: 2 * time.Minute,
		Transport: hostMapTransport{
			base:  http.DefaultTransport,
			hosts: map[string]string{"cdn.modrinth.test": jarFile.URL},
		},
	})
	fetcher := &blockingOptimizationFetcher{started: make(chan string, 2), release: make(chan struct{})}
	svc := NewModService(store, nil, nil, logger.New())
	svc.optimizationFetch = fetcher.Fetch
	svc.optimizationClient = client
	return svc, server.ID, fetcher
}

func TestFabricOptimizationInstallsSerializePerServer(t *testing.T) {
	ctx := context.Background()
	svc, serverID, fetcher := newFabricOptimizationTestService(t)

	request := &v1.InstallFabricOptimizationStackRequest{ServerId: serverID, ModIds: []string{"lithium"}}
	results := make(chan error, 2)
	go func() {
		_, err := svc.InstallFabricOptimizationStack(ctx, connect.NewRequest(request))
		results <- err
	}()
	select {
	case <-fetcher.started:
	case <-time.After(10 * time.Second):
		t.Fatal("first install never reached the provider")
	}
	go func() {
		_, err := svc.InstallFabricOptimizationStack(ctx, connect.NewRequest(request))
		results <- err
	}()
	select {
	case slug := <-fetcher.started:
		t.Fatalf("second same-server install started %q before the first released the server lock", slug)
	case <-time.After(200 * time.Millisecond):
	}
	close(fetcher.release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("install did not finish")
		}
	}
	fetcher.mu.Lock()
	calls := fetcher.calls
	fetcher.mu.Unlock()
	require.Equal(t, 2, calls)
}

func TestFabricOptimizationInstallsRunIndependentlyPerServer(t *testing.T) {
	ctx := context.Background()
	svc, firstServerID, fetcher := newFabricOptimizationTestService(t)
	second := &storage.Server{
		ID: "fabric-optimization-second", Name: "fabric-optimization-second",
		DataPath: t.TempDir(), MCVersion: "1.21.1", ModLoader: storage.ModLoaderFabric,
		Status: storage.StatusStopped,
	}
	require.NoError(t, svc.store.CreateServer(ctx, second))

	results := make(chan error, 2)
	start := func(serverID string) {
		_, err := svc.InstallFabricOptimizationStack(ctx, connect.NewRequest(&v1.InstallFabricOptimizationStackRequest{
			ServerId: serverID, ModIds: []string{"lithium"},
		}))
		results <- err
	}
	go start(firstServerID)
	go start(second.ID)
	for i := 0; i < 2; i++ {
		select {
		case <-fetcher.started:
		case <-time.After(10 * time.Second):
			t.Fatal("an independent server was blocked behind another server's install")
		}
	}
	close(fetcher.release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("install did not finish")
		}
	}
}

func TestFabricOptimizationDirectInstallsSerializePerModsDir(t *testing.T) {
	var mu sync.Mutex
	requests := 0
	requestStarted := make(chan int, 2)
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		requests++
		count := requests
		mu.Unlock()
		requestStarted <- count
		if count == 1 {
			<-release
		}
		_, _ = w.Write([]byte("jar"))
	}))
	defer server.Close()
	mods := t.TempDir()
	client := indexers.ProtectCredentialsOnRedirect(&http.Client{
		Timeout: 2 * time.Minute,
		Transport: hostMapTransport{
			base:  http.DefaultTransport,
			hosts: map[string]string{"cdn.modrinth.test": server.URL},
		},
	})
	results := make(chan error, 2)
	go func() {
		results <- installFabricOptimizationJarWithClient(context.Background(), mods,
			modrinthVersionFile{URL: "https://cdn.modrinth.test/lithium-2.0.jar", Filename: "lithium-2.0.jar"}, "lithium", client)
	}()
	select {
	case count := <-requestStarted:
		require.Equal(t, 1, count)
	case <-time.After(10 * time.Second):
		t.Fatal("first direct install never started")
	}
	go func() {
		results <- installFabricOptimizationJarWithClient(context.Background(), mods,
			modrinthVersionFile{URL: "https://cdn.modrinth.test/lithium-3.0.jar", Filename: "lithium-3.0.jar"}, "lithium", client)
	}()
	select {
	case count := <-requestStarted:
		t.Fatalf("second direct install reached the provider (%d) before the first released the mods-dir lock", count)
	case <-time.After(200 * time.Millisecond):
	}
	close(release)
	for i := 0; i < 2; i++ {
		select {
		case err := <-results:
			require.NoError(t, err)
		case <-time.After(10 * time.Second):
			t.Fatal("direct install did not finish")
		}
	}
	require.FileExists(t, filepath.Join(mods, "lithium-3.0.jar"))
}
