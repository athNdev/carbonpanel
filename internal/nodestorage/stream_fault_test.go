// SPDX-License-Identifier: MPL-2.0
package nodestorage

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/athNdev/carbon-panel/internal/storageworker"
)

type frameFaultWriter struct {
	io.Writer
	mutate func(*storageworker.Request)
}

type frameFaultReader struct {
	scanner *bufio.Scanner
	pending []byte
	done    bool
}

func (r *frameFaultReader) Read(p []byte) (int, error) {
	if len(r.pending) == 0 {
		if !r.scanner.Scan() {
			if err := r.scanner.Err(); err != nil {
				return 0, err
			}
			return 0, io.EOF
		}
		var res storageworker.Response
		if err := json.Unmarshal(r.scanner.Bytes(), &res); err != nil {
			return 0, err
		}
		if len(res.Data) > 0 && !r.done {
			res.Data[0] ^= 1
			r.done = true
		}
		b, err := json.Marshal(res)
		if err != nil {
			return 0, err
		}
		r.pending = append(b, '\n')
	}
	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

func (f frameFaultWriter) Write(p []byte) (int, error) {
	var r storageworker.Request
	if err := json.Unmarshal(p, &r); err != nil {
		return 0, err
	}
	f.mutate(&r)
	b, err := json.Marshal(r)
	if err != nil {
		return 0, err
	}
	b = append(b, '\n')
	if _, err = f.Writer.Write(b); err != nil {
		return 0, err
	}
	return len(p), nil
}

func TestProcessStreamRejectsCorruptionAndCancellation(t *testing.T) {
	bin := carbonStorageBin(t)
	for _, fault := range []string{"checksum", "sequence", "oversized", "commit", "readchecksum", "cancel"} {
		t.Run(fault, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "target")
			if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			cmd := exec.Command(bin, "--mode=accessor", "--root", root)
			in, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			out, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			wire := frameFaultWriter{Writer: in, mutate: func(r *storageworker.Request) {
				if fault == "checksum" && r.Op == "stream_write" {
					r.Checksum = "corrupt"
				}
				if fault == "sequence" && r.Op == "stream_write" {
					r.Sequence++
				}
				if fault == "oversized" && r.Op == "stream_write" {
					r.Data = make([]byte, storageworker.ChunkBytes+1)
				}
				if fault == "commit" && r.Op == "stream_commit" {
					r.Checksum = "corrupt"
				}
			}}
			var response io.Reader = out
			if fault == "readchecksum" {
				scanner := bufio.NewScanner(out)
				scanner.Buffer(make([]byte, 4096), 1<<20)
				response = &frameFaultReader{scanner: scanner}
			}
			sess, err := storageworker.NewSessionContext(ctx, wire, response, func() error { _ = in.Close(); _ = out.Close(); return cmd.Wait() })
			if err != nil {
				t.Fatal(err)
			}
			defer sess.Close()
			acc := storageworker.NewRemoteAccessor(sess)
			w, err := acc.BeginWrite(ctx, "target", 1024)
			if err != nil {
				t.Fatal(err)
			}
			_, err = w.Write([]byte("new"))
			if fault == "checksum" || fault == "sequence" || fault == "oversized" {
				if err == nil {
					t.Fatal("invalid frame accepted")
				}
			} else if fault == "commit" {
				if err != nil {
					t.Fatal(err)
				}
				if err = w.Commit(); err == nil {
					t.Fatal("invalid digest accepted")
				}
			} else if fault == "readchecksum" {
				if err != nil {
					t.Fatal(err)
				}
				_ = w.Close()
				r, err := acc.OpenRead(ctx, "target")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := io.ReadAll(r); err == nil {
					t.Fatal("corrupt worker response accepted")
				}
				_ = r.Close()
			} else {
				if err != nil {
					t.Fatal(err)
				}
				cancel()
				_ = sess.Close()
			}
			_ = w.Close()
			b, err := os.ReadFile(target)
			if err != nil || string(b) != "old" {
				t.Fatalf("target changed under %s: %q %v", fault, b, err)
			}
			// EOF cleanup releases the current transaction on cancellation; invalid
			// frames abort only that handle while keeping valid session frames usable.
			matches, _ := filepath.Glob(filepath.Join(root, ".carbon-write-*.partial"))
			if len(matches) != 0 {
				t.Fatalf("owned staging leaked %v", matches)
			}
			if fault != "cancel" {
				if err := acc.WriteFile("healthy", []byte("ok")); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestProcessStreamHandleCapAndRandomAccess(t *testing.T) {
	node := NodeInfo{Name: "worker", StorageRoot: t.TempDir()}
	root := nodeDataRoot(node, "srv")
	_ = os.MkdirAll(root, 0700)
	acc, err := (&ProcessTransport{Bin: carbonStorageBin(t)}).Open(context.Background(), node, "srv")
	if err != nil {
		t.Fatal(err)
	}
	defer acc.Close()
	if err := acc.WriteFile("file", []byte("0123456789")); err != nil {
		t.Fatal(err)
	}
	var readers []io.ReadCloser
	for i := 0; i < 16; i++ {
		r, err := acc.OpenRead(context.Background(), "file")
		if err != nil {
			t.Fatal(err)
		}
		readers = append(readers, r)
	}
	if r, err := acc.OpenRead(context.Background(), "file"); err == nil {
		_ = r.Close()
		t.Fatal("unbounded handle count")
	} else if !strings.Contains(err.Error(), "too many") {
		t.Fatal(err)
	}
	r := readers[0]
	buf := make([]byte, 3)
	if n, err := r.(io.ReaderAt).ReadAt(buf, 6); err != nil || n != 3 || string(buf) != "678" {
		t.Fatalf("random read %q %d %v", buf, n, err)
	}
	for _, r := range readers {
		if err := r.Close(); err != nil {
			t.Fatal(err)
		}
	}
	next, err := acc.OpenRead(context.Background(), "file")
	if err != nil {
		t.Fatal("closed handles were not released", err)
	}
	_ = next.Close()
}
