// SPDX-License-Identifier: MPL-2.0
package module

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/docker"
	"github.com/athNdev/carbon-panel/pkg/logger"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

type hookExecConfig struct {
	Cmd          []string
	AttachStdin  bool
	AttachStdout bool
	AttachStderr bool
	Tty          bool
}

// The two real HTTP daemons distinguish a correctly routed hook from a
// successful command on the controller. Both listen only on loopback.
type hookDaemon struct {
	mu          sync.Mutex
	requests    []string
	creates     []hookExecConfig
	exitCode    int
	failCreate  bool
	blockCreate bool
}

func newHookDaemon(t *testing.T, daemon *hookDaemon) *docker.Client {
	t.Helper()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/_ping" {
			w.Header().Set("Api-Version", "1.45")
			w.WriteHeader(http.StatusOK)
			return
		}
		daemon.mu.Lock()
		daemon.requests = append(daemon.requests, r.Method+" "+r.URL.Path)
		daemon.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/containers/module-container/exec"):
			var cfg hookExecConfig
			if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			daemon.mu.Lock()
			daemon.creates = append(daemon.creates, cfg)
			daemon.mu.Unlock()
			if daemon.blockCreate {
				<-r.Context().Done()
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if daemon.failCreate {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = w.Write([]byte(`{"message":"assigned daemon unavailable"}`))
				return
			}
			_, _ = w.Write([]byte(`{"Id":"hook-exec"}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/exec/hook-exec/start"):
			if _, err := io.Copy(io.Discard, r.Body); err != nil {
				t.Errorf("read exec attach request: %v", err)
				return
			}
			conn, buf, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Errorf("hijack exec stream: %v", err)
				return
			}
			defer func() { _ = conn.Close() }()
			_, _ = buf.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
			_ = buf.Flush()
			// Docker's non-TTY stream prefixes stdout with its type and length.
			output := []byte("hook output")
			header := make([]byte, 8)
			header[0] = 1
			binary.BigEndian.PutUint32(header[4:], uint32(len(output)))
			_, _ = conn.Write(append(header, output...))
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/exec/hook-exec/json"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"ID":"hook-exec","Running":false,"ExitCode":%d}`, daemon.exitCode)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	cli, err := docker.NewClient(strings.Replace(api.URL, "http://", "tcp://", 1), logger.New())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func (d *hookDaemon) snapshot() ([]string, []hookExecConfig) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.requests...), append([]hookExecConfig(nil), d.creates...)
}

func newHookManager(local, remote *docker.Client) *Manager {
	pool := docker.NewClientPool(nil, local, logger.New())
	if remote != nil {
		pool.RegisterClient("remote-node", remote)
	}
	return NewManagerWithPool(nil, local, pool, nil, nil, nil, logger.New())
}

func TestHookExecUsesAssignedNode(t *testing.T) {
	local, remote := &hookDaemon{}, &hookDaemon{}
	m := newHookManager(newHookDaemon(t, local), newHookDaemon(t, remote))
	mod := &storage.Module{ID: "module-id", Name: "remote-hook", NodeID: "remote-node", ContainerID: "module-container"}
	command := "printf 'keep spaces; no implicit shell'"
	m.executeHook(context.Background(), mod, &v1.ModuleEventHook{
		Action: v1.ModuleEventAction_MODULE_EVENT_ACTION_EXEC, Command: command,
	}, "parent-server")
	if requests, _ := local.snapshot(); len(requests) != 0 {
		t.Fatalf("remote hook touched the local daemon: %v", requests)
	}
	requests, creates := remote.snapshot()
	if len(requests) != 3 || len(creates) != 1 {
		t.Fatalf("remote exec journey = %v, creates %v", requests, creates)
	}
	want := hookExecConfig{Cmd: []string{command}, AttachStdin: true, AttachStdout: true, AttachStderr: true}
	if !reflect.DeepEqual(creates[0], want) {
		t.Fatalf("exec configuration = %+v, want %+v", creates[0], want)
	}
}

func TestHookExecUnresolvedNodeNeverTouchesLocal(t *testing.T) {
	local := &hookDaemon{}
	m := newHookManager(newHookDaemon(t, local), nil)
	err := m.execInModule(context.Background(), &storage.Module{NodeID: "missing-node", ContainerID: "module-container"}, "task")
	if !errors.Is(err, docker.ErrUnknownNode) {
		t.Errorf("error = %v, want unknown assigned node", err)
	}
	if requests, _ := local.snapshot(); len(requests) != 0 {
		t.Fatalf("unresolved hook touched the local daemon: %v", requests)
	}
}

func TestHookExecRemoteWithoutLocalClient(t *testing.T) {
	remote := &hookDaemon{}
	m := newHookManager(nil, newHookDaemon(t, remote))
	err := m.execInModule(context.Background(), &storage.Module{NodeID: "remote-node", ContainerID: "module-container"}, "task")
	if err != nil {
		t.Fatal(err)
	}
	if requests, _ := remote.snapshot(); len(requests) != 3 {
		t.Fatalf("remote exec journey = %v", requests)
	}
}

func TestHookExecRemoteFailureNeverFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remote *hookDaemon
		want   string
	}{
		{name: "unavailable", remote: &hookDaemon{failCreate: true}, want: "assigned daemon unavailable"},
		{name: "command failed", remote: &hookDaemon{exitCode: 7}, want: "exit code 7: hook output"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			local := &hookDaemon{}
			m := newHookManager(newHookDaemon(t, local), newHookDaemon(t, tc.remote))
			err := m.execInModule(context.Background(), &storage.Module{NodeID: "remote-node", ContainerID: "module-container"}, "task")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
			if requests, _ := local.snapshot(); len(requests) != 0 {
				t.Fatalf("failed remote hook touched local daemon: %v", requests)
			}
		})
	}
}

func TestHookExecCancellation(t *testing.T) {
	t.Run("cancelled before resolution", func(t *testing.T) {
		local, remote := &hookDaemon{}, &hookDaemon{}
		m := newHookManager(newHookDaemon(t, local), newHookDaemon(t, remote))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		err := m.execInModule(ctx, &storage.Module{NodeID: "remote-node", ContainerID: "module-container"}, "task")
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error = %v, want cancellation", err)
		}
		if m.pool.Stats().TotalRequests != 0 {
			t.Fatal("cancelled hook entered node resolution")
		}
		for _, daemon := range []*hookDaemon{local, remote} {
			if requests, _ := daemon.snapshot(); len(requests) != 0 {
				t.Fatalf("cancelled hook contacted a daemon: %v", requests)
			}
		}
	})
	t.Run("deadline reaches remote daemon", func(t *testing.T) {
		local, remote := &hookDaemon{}, &hookDaemon{blockCreate: true}
		m := newHookManager(newHookDaemon(t, local), newHookDaemon(t, remote))
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		start := time.Now()
		err := m.execInModule(ctx, &storage.Module{NodeID: "remote-node", ContainerID: "module-container"}, "task")
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want deadline", err)
		}
		if time.Since(start) > 2*time.Second {
			t.Fatal("remote exec ignored operation deadline")
		}
		if requests, _ := local.snapshot(); len(requests) != 0 {
			t.Fatalf("remote deadline touched local daemon: %v", requests)
		}
		if requests, _ := remote.snapshot(); len(requests) != 1 {
			t.Fatalf("remote requests = %v, want create only", requests)
		}
	})
}

func TestHookExecLocalAliasesAndMissingContainer(t *testing.T) {
	for _, node := range []string{"", "local", "default"} {
		t.Run("local alias "+node, func(t *testing.T) {
			local := &hookDaemon{}
			m := newHookManager(newHookDaemon(t, local), nil)
			if err := m.execInModule(context.Background(), &storage.Module{NodeID: node, ContainerID: "module-container"}, "task"); err != nil {
				t.Fatal(err)
			}
			if requests, _ := local.snapshot(); len(requests) != 3 {
				t.Fatalf("local exec journey = %v", requests)
			}
		})
	}
	t.Run("no container is still a no-op", func(t *testing.T) {
		if err := (&Manager{}).execInModule(context.Background(), &storage.Module{NodeID: "unavailable-node"}, "task"); err != nil {
			t.Fatal(err)
		}
	})
}
