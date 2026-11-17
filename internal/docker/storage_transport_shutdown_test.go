// SPDX-License-Identifier: MPL-2.0
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/moby/moby/client"
)

// The real SDK attach path carries the confined worker protocol. This fixture
// proves shutdown ordering and filesystem cleanup, not a live Docker run.
func TestStorageWorkerShutdownWaitsForOwnedStreamAbort(t *testing.T) {
	for _, stalledWait := range []bool{false, true} {
		name := "graceful"
		if stalledWait {
			name = "wait timeout still removes owned container"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			fs, err := nodestorage.NewLocal(root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = fs.Close() }()
			if err := fs.WriteFile("target", []byte("old")); err != nil {
				t.Fatal(err)
			}
			stopped := make(chan struct{})
			var waited, removed atomic.Bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("Api-Version", "1.45")
					w.Header().Set("Ostype", "linux")
				case strings.HasSuffix(r.URL.Path, "/containers/create"):
					var config struct{ StdinOnce bool }
					if err := json.NewDecoder(r.Body).Decode(&config); err != nil || !config.StdinOnce {
						t.Errorf("worker must close stdin on detach: %v, %v", config, err)
					}
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"Id":"owned-stream-worker"}`)
				case strings.HasSuffix(r.URL.Path, "/containers/owned-stream-worker/attach"):
					conn, rw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_, _ = fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
					_ = rw.Flush()
					go func() {
						defer func() { _ = conn.Close() }()
						defer close(stopped)
						_ = storageworker.ServeAccessor(rw.Reader, dockerStdoutFrame{conn}, fs)
					}()
				case strings.HasSuffix(r.URL.Path, "/containers/owned-stream-worker/start"):
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/containers/owned-stream-worker/wait"):
					waited.Store(true)
					if stalledWait {
						<-r.Context().Done()
						return
					}
					select {
					case <-stopped:
					case <-r.Context().Done():
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"StatusCode":0}`)
				case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/owned-stream-worker"):
					if !waited.Load() {
						t.Error("force removal preceded graceful exit wait")
					}
					select {
					case <-stopped:
					default:
						t.Error("worker removed before EOF cleanup")
					}
					partials, err := filepath.Glob(filepath.Join(root, ".carbon-write-*.partial"))
					if err != nil || len(partials) != 0 {
						t.Errorf("owned staging not cleaned before removal: %v,%v", partials, err)
					}
					removed.Store(true)
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			sdk, err := NewAPIClient(client.WithHost(fakeDockerHost(t, server)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sdk.Close() }()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			session, err := (&Client{docker: sdk}).startStorageWorker(ctx, "fixture", root)
			if err != nil {
				t.Fatal(err)
			}
			accessor := storageworker.NewRemoteAccessor(session)
			writer, err := accessor.BeginWrite(ctx, "target", 1024)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = writer.Write([]byte("new")); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			cancel()
			err = accessor.Close()
			_ = writer.Close()
			if stalledWait {
				if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "recovery staging") {
					t.Fatalf("missing bounded cleanup warning: %v", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatal("shutdown exceeded bounded grace")
			}
			if !removed.Load() {
				t.Fatal("owned container not removed")
			}
			body, err := os.ReadFile(filepath.Join(root, "target"))
			if err != nil || string(body) != "old" {
				t.Fatalf("old target changed: %q,%v", body, err)
			}
		})
	}
}
