// SPDX-License-Identifier: MPL-2.0
package docker

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/moby/moby/client"
)

type storageZeros struct{}

type dockerStdoutFrame struct{ io.Writer }

func (w dockerStdoutFrame) Write(p []byte) (int, error) {
	var header [8]byte
	header[0] = 1
	binary.BigEndian.PutUint32(header[4:], uint32(len(p)))
	if _, err := w.Writer.Write(header[:]); err != nil {
		return 0, err
	}
	return w.Writer.Write(p)
}

func (storageZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// This exercises the production Docker SDK attach/demultiplex path against an
// isolated HTTP engine fixture serving the real confined worker implementation.
// It does not require or mutate a running Docker daemon.
func TestStorageWorkerDockerAttachLargeStream(t *testing.T) {
	fs, err := nodestorage.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	workerDone := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/_ping":
			w.Header().Set("Api-Version", "1.45")
			w.Header().Set("Ostype", "linux")
		case strings.HasSuffix(r.URL.Path, "/containers/create"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"Id":"stream-fixture","Warnings":[]}`)
		case strings.HasSuffix(r.URL.Path, "/containers/stream-fixture/attach"):
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				workerDone <- err
				return
			}
			_, _ = fmt.Fprint(rw, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = rw.Flush()
			go func() {
				defer conn.Close()
				workerDone <- storageworker.ServeAccessor(rw.Reader, dockerStdoutFrame{conn}, fs)
			}()
		case strings.HasSuffix(r.URL.Path, "/containers/stream-fixture/start"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/stream-fixture"):
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	sdk, err := NewAPIClient(client.WithHost(fakeDockerHost(t, server)))
	if err != nil {
		t.Fatal(err)
	}
	defer sdk.Close()
	sess, err := (&Client{docker: sdk}).startStorageWorker(context.Background(), "isolated-fixture", fs.Root())
	if err != nil {
		t.Fatal(err)
	}
	acc := storageworker.NewRemoteAccessor(sess)
	defer acc.Close()
	const size int64 = 130 << 20
	out, err := acc.BeginWrite(context.Background(), "large", size)
	if err != nil {
		t.Fatal(err)
	}
	expected := sha256.New()
	buf := make([]byte, storageworker.ChunkBytes)
	if n, err := io.CopyBuffer(io.MultiWriter(out, expected), io.LimitReader(storageZeros{}, size), buf); err != nil || n != size {
		t.Fatalf("write %d: %v", n, err)
	}
	if err := out.Commit(); err != nil {
		t.Fatal(err)
	}
	_ = out.Close()
	in, err := acc.OpenRead(context.Background(), "large")
	if err != nil {
		t.Fatal(err)
	}
	actual := sha256.New()
	if n, err := io.CopyBuffer(actual, in, buf); err != nil || n != size {
		t.Fatalf("read %d: %v", n, err)
	}
	_ = in.Close()
	if string(expected.Sum(nil)) != string(actual.Sum(nil)) {
		t.Fatal("Docker attach changed stream content")
	}
	if err := acc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-workerDone; err != nil {
		t.Fatal(err)
	}
}
