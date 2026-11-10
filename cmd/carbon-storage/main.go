// carbon-storage is a line-oriented JSON worker for worldstore backup I/O and
// for nodestorage accessor mode over Docker attach/exec (OSS-281).
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	"github.com/athNdev/carbon-panel/internal/worldstore"
)

type request struct {
	Op       string              `json:"op"`
	Identity worldstore.Identity `json:"identity"`
	Path     string              `json:"path,omitempty"`
	Data     []byte              `json:"data,omitempty"`
}

type response struct {
	OK      bool               `json:"ok"`
	Error   string             `json:"error,omitempty"`
	Entries []worldstore.Entry `json:"entries,omitempty"`
	Data    []byte             `json:"data,omitempty"`
	SHA256  string             `json:"sha256,omitempty"`
}

func main() {
	var mode, root, journal, server, node, daemon, owner string
	var generation uint64
	flag.StringVar(&mode, "mode", "worldstore", "worldstore or accessor")
	flag.StringVar(&root, "root", "", "authorized server data root")
	flag.StringVar(&journal, "journal-dir", "", "durable operation journal directory (worldstore mode)")
	flag.StringVar(&server, "server", "", "bound server identity")
	flag.StringVar(&node, "node", "", "bound node identity")
	flag.StringVar(&daemon, "daemon", "", "bound daemon identity")
	flag.StringVar(&owner, "owner", "", "bound owner identity")
	flag.Uint64Var(&generation, "generation", 0, "bound nonzero assignment generation")
	flag.Parse()
	if root == "" {
		fmt.Fprintln(os.Stderr, "--root is required")
		os.Exit(2)
	}
	switch mode {
	case "accessor":
		acc, err := nodestorage.NewLocal(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer func() { _ = acc.Close() }()
		if err := storageworker.ServeAccessor(os.Stdin, os.Stdout, acc); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	case "worldstore":
		if journal == "" {
			fmt.Fprintln(os.Stderr, "--journal-dir is required in worldstore mode")
			os.Exit(2)
		}
		store, err := worldstore.NewLocal(worldstore.Identity{Server: server, Node: node, Daemon: daemon, Owner: owner, Generation: generation}, root, journal)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer func() {
			if cerr := store.Close(); cerr != nil {
				fmt.Fprintln(os.Stderr, cerr)
			}
		}()
		if err := serveWorldstore(os.Stdin, os.Stdout, store); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown --mode %q\n", mode)
		os.Exit(2)
	}
}

func serveWorldstore(input io.Reader, output io.Writer, store *worldstore.Local) (err error) {
	scanner := bufio.NewScanner(input)
	// ZIP payloads are bounded at 512 MiB; JSON base64 adds roughly one third.
	scanner.Buffer(make([]byte, 64*1024), 700<<20)
	w := bufio.NewWriter(output)
	defer func() {
		if ferr := w.Flush(); ferr != nil && err == nil {
			err = ferr
		}
	}()
	enc := json.NewEncoder(w)
	for scanner.Scan() {
		var req request
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			if e := enc.Encode(response{Error: err.Error()}); e != nil {
				return e
			}
			continue
		}
		res := dispatchWorldstore(store, req)
		if err := enc.Encode(res); err != nil {
			return err
		}
		if err := w.Flush(); err != nil {
			return err
		}
	}
	return scanner.Err()
}

func dispatchWorldstore(s *worldstore.Local, r request) response {
	res := response{}
	var err error
	switch r.Op {
	case "list":
		res.Entries, err = s.List(r.Identity)
	case "read":
		res.Data, err = s.Read(r.Identity, r.Path)
	case "upload":
		err = s.Upload(r.Identity, r.Path, r.Data)
	case "backup":
		var a worldstore.Archive
		a, err = s.Backup(r.Identity)
		res.Data, res.SHA256 = a.Data, a.SHA256
	case "restore":
		err = s.Restore(r.Identity, r.Data)
	default:
		err = fmt.Errorf("unsupported operation %q", r.Op)
	}
	if err != nil {
		res.Error = err.Error()
		return res
	}
	res.OK = true
	return res
}
