// SPDX-License-Identifier: MPL-2.0
package docker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// Exercise real Docker HTTP requests: cancellation after creation must not
// prevent cleanup of that exact created container with a fresh context.
func TestStorageWorkerStartupCancellationRemovesCreatedContainer(t *testing.T) {
	for _, fixture := range []struct {
		name, stage string
		failRemoval bool
	}{
		{"attach", "attach", false},
		{"start", "start", false},
		{"failed removal is reported", "attach", true},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			removed := make(chan string, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/_ping":
					w.Header().Set("Api-Version", "1.45")
					w.Header().Set("Ostype", "linux")
					w.WriteHeader(http.StatusOK)
				case strings.HasSuffix(r.URL.Path, "/containers/create"):
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusCreated)
					_, _ = io.WriteString(w, `{"Id":"owned-container-123","Warnings":[]}`)
				case strings.HasSuffix(r.URL.Path, "/containers/owned-container-123/attach"):
					if fixture.stage == "attach" {
						cancel()
						w.WriteHeader(http.StatusServiceUnavailable)
						return
					}
					conn, rw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Errorf("hijack attach: %v", err)
						return
					}
					_, _ = fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
					_ = rw.Flush()
					go func() { _, _ = io.Copy(io.Discard, conn); _ = conn.Close() }()
				case strings.HasSuffix(r.URL.Path, "/containers/owned-container-123/start"):
					cancel()
					select {
					case <-r.Context().Done():
					case <-time.After(3 * time.Second):
					}
				case r.Method == http.MethodDelete && strings.Contains(r.URL.Path, "/containers/"):
					removed <- r.URL.Path + "?" + r.URL.RawQuery
					if fixture.failRemoval {
						http.Error(w, "cleanup refused", http.StatusInternalServerError)
						return
					}
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.String())
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			sdk, err := NewAPIClient(client.WithHost(fakeDockerHost(t, server)))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = sdk.Close() }()
			_, err = (&Client{docker: sdk}).startStorageWorker(ctx, "fixture-image", t.TempDir())
			if err == nil || !errors.Is(ctx.Err(), context.Canceled) {
				t.Fatalf("startup error=%v context error=%v, want failure after cancellation", err, ctx.Err())
			}
			if fixture.failRemoval && !strings.Contains(err.Error(), "remove storage worker") {
				t.Fatalf("cleanup failure not reported: %v", err)
			}
			select {
			case path := <-removed:
				if !strings.Contains(path, "/containers/owned-container-123?") || !strings.Contains(path, "force=1") {
					t.Fatalf("unexpected cleanup target: %s", path)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("created worker container was not removed after cancellation")
			}
		})
	}
}
