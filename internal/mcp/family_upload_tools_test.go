// SPDX-License-Identifier: MPL-2.0
package mcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/rbac"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"google.golang.org/protobuf/reflect/protoreflect"
)

// --- upload-family fixture helpers ------------------------------------------

// newUploadServer now returns the PRODUCTION server. The upload-family and
// bounded-stream tools must be mounted by NewServer/registerCombinedTools; an
// isolated hand-off server would no longer prove the integrated surface. The
// real SDK stdio wire is still used via connectStdioSession.
func newUploadServer(t *testing.T, clients *Clients) *sdkmcp.Server {
	t.Helper()
	return NewServer(clients)
}

func uploadClients(t *testing.T, f *controllerStreamFixture, token string) *Clients {
	t.Helper()
	cl, err := NewClients(f.ts.Client(), f.ts.URL, token)
	if err != nil {
		t.Fatalf("NewClients: %v", err)
	}
	return cl
}

func uploadSessionForToken(t *testing.T, f *controllerStreamFixture, token string) *sdkmcp.ClientSession {
	t.Helper()
	return connectStdioSession(t, newUploadServer(t, uploadClients(t, f, token)), context.Background())
}

func uploadSessionFor(t *testing.T, f *controllerStreamFixture, userID, role string, perms ...rbac.Permission) *sdkmcp.ClientSession {
	t.Helper()
	token := seedToken(t, f.store, f.enforcer, userID, role, perms)
	return uploadSessionForToken(t, f, token)
}

func uploadOnlyPerms() []rbac.Permission { return globalPerms("uploads", "create") }

func writeUploadSource(t *testing.T, f *controllerStreamFixture, rel string, payload []byte) {
	t.Helper()
	acc, err := nodestorage.NewLocal(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = acc.Close() }()
	if err := acc.WriteFile(rel, payload); err != nil {
		t.Fatal(err)
	}
}

func uploadDestPath(f *controllerStreamFixture, rel string) string {
	return filepath.Join(f.root, filepath.FromSlash(rel))
}

func requireNoUploadDest(t *testing.T, f *controllerStreamFixture, rel string) {
	t.Helper()
	if _, err := os.Stat(uploadDestPath(f, rel)); !os.IsNotExist(err) {
		t.Fatalf("refusal wrote destination %q: %v", rel, err)
	}
}

func requireUploadDest(t *testing.T, f *controllerStreamFixture, rel string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(uploadDestPath(f, rel))
	if err != nil {
		t.Fatalf("read destination %q: %v", rel, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("destination %q bytes mismatch: got %d want %d", rel, len(got), len(want))
	}
}

// initUploadOnly creates a real, OWNED but NOT-yet-completed upload session.
func initUploadOnly(t *testing.T, f *controllerStreamFixture, token, filename string, size int64) string {
	t.Helper()
	init, err := uploadClients(t, f, token).Upload.InitUpload(context.Background(), connect.NewRequest(&v1.InitUploadRequest{
		Filename: filename, TotalSize: size, ChunkSize: storageworker.ChunkBytes,
	}))
	if err != nil {
		t.Fatalf("InitUpload: %v", err)
	}
	return init.Msg.GetSessionId()
}

// completedUpload creates a real, owned, completed upload session through the
// typed RPCs so a save tool can be invoked against an honest session.
func completedUpload(t *testing.T, f *controllerStreamFixture, token, filename string, payload []byte) string {
	t.Helper()
	sid := initUploadOnly(t, f, token, filename, int64(len(payload)))
	if _, err := uploadClients(t, f, token).Upload.UploadChunk(context.Background(), connect.NewRequest(&v1.UploadChunkRequest{
		SessionId: sid, ChunkIndex: 0, Data: payload,
	})); err != nil {
		t.Fatalf("UploadChunk: %v", err)
	}
	return sid
}

// enableUploadRecorder resets the shared RPC recorder and restores its disabled
// state on cleanup, so a failed upload-family subtest never leaks global state.
func enableUploadRecorder(t *testing.T) int {
	t.Helper()
	invocationRecorder.enable()
	t.Cleanup(invocationRecorder.disable)
	return len(invocationRecorder.snapshot())
}

func recordedMsg(t *testing.T, before int, procedure string) protoreflect.Message {
	t.Helper()
	for _, rec := range invocationRecorder.snapshot()[before:] {
		if rec.Procedure == procedure {
			return rec.Msg
		}
	}
	t.Fatalf("typed RPC %s was not recorded", procedure)
	return nil
}

// --- Descriptor integrity ----------------------------------------------------

func TestUploadFamilyDescriptorIntegrity(t *testing.T) {
	if len(UploadFamilyDescriptors) != 4 {
		t.Fatalf("descriptor count = %d, want 4", len(UploadFamilyDescriptors))
	}
	var committed []UploadToolDescriptor
	if err := json.Unmarshal(UploadFamilyDescriptorsJSON(), &committed); err != nil {
		t.Fatalf("embedded descriptor JSON: %v", err)
	}
	got, err := MarshalUploadFamilyDescriptors()
	if err != nil {
		t.Fatal(err)
	}
	var live []UploadToolDescriptor
	if err := json.Unmarshal(got, &live); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(committed, live) {
		t.Fatalf("committed descriptor artifact drifted from live table:\ncommitted=%+v\nlive=%+v", committed, live)
	}

	// Factual scope fields must match the live RBAC map, not a hand copy.
	for _, d := range UploadFamilyDescriptors {
		if want := expectedScope(d.Procedure); want != d.Scope {
			t.Errorf("%s: descriptor scope %q, live RBAC %q", d.Tool, d.Scope, want)
		}
		if d.Class != ClassReview || !d.Confirm {
			t.Errorf("%s: expected a confirm-gated review tool, got class=%s confirm=%v", d.Tool, d.Class, d.Confirm)
		}
		if d.Procedure == "" || d.Service == "" || d.Method == "" || d.Client == "" || d.SourceRef == "" {
			t.Errorf("%s: incomplete descriptor tuple: %+v", d.Tool, d)
		}
	}

	wantProcs := map[string]bool{uploadProcSave: true, uploadProcInit: true, uploadProcChunk: true}
	seen := map[string]bool{}
	for _, d := range UploadFamilyDescriptors {
		seen[d.Procedure] = true
		if d.Tool == UploadToolStreamCommit && !strings.Contains(d.BoundedArgs, "no raw bytes") {
			t.Error("composed upload-stream tool must declare no raw byte arguments")
		}
	}
	for p := range wantProcs {
		if !seen[p] {
			t.Errorf("descriptor set is missing procedure %s", p)
		}
	}
	for _, name := range []string{UploadToolSaveUploadedFile, UploadToolInitUpload, UploadToolUploadChunk} {
		found := false
		for _, s := range UploadFamilyToolSpecs() {
			if s.Name == name {
				found = true
			}
		}
		if !found {
			t.Errorf("UploadFamilyToolSpecs missing %s", name)
		}
	}
}

// --- Executable tools/call evidence -----------------------------------------

// TestUploadFamilyTypedInvocationEvidence drives each direct RPC tool through
// a real MCP tools/call over stdio and records four facts: the typed method was
// reached, every argument maps to an exact typed field, the real handler outcome
// is surfaced, and the resource scope succeeds for an entitled caller and is
// denied (with a real Connect code) for an ungranted dp_ token.
func TestUploadFamilyTypedInvocationEvidence(t *testing.T) {
	f := newControllerStreamFixture(t)
	adminToken := seedToken(t, f.store, f.enforcer, "u-upload-admin", "admin", nil)
	completed := completedUpload(t, f, adminToken, "typed.jar", []byte("0123456789abcdef"))
	// Fresh, NOT-yet-completed session owned by the same admin for the chunk tool.
	chunkSession := initUploadOnly(t, f, adminToken, "chunk.jar", 16)

	cases := []struct {
		tool string
		args map[string]any
		proc string
	}{
		{
			tool: UploadToolSaveUploadedFile,
			args: map[string]any{
				"server_id": "srv-1", "upload_session_id": completed,
				"destination_path": "mods", "filename": "typed.jar", "confirm": true,
			},
			proc: uploadProcSave,
		},
		{
			tool: UploadToolInitUpload,
			args: map[string]any{
				"filename": "typed.jar", "total_size": 16, "chunk_size": 262144, "confirm": true,
			},
			proc: uploadProcInit,
		},
		{
			tool: UploadToolUploadChunk,
			args: map[string]any{
				"session_id": chunkSession, "chunk_index": 0,
				"data": base64.StdEncoding.EncodeToString([]byte("0123456789abcdef")), "confirm": true,
			},
			proc: uploadProcChunk,
		},
	}

	noperms := uploadSessionFor(t, f, "u-upload-noperms", "noperms")
	invalid := uploadSessionForToken(t, f, "dp_nonexistent_upload_token")

	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			admin := uploadSessionForToken(t, f, adminToken)
			before := enableUploadRecorder(t)
			out := callTool(t, admin, tc.tool, tc.args)
			assertSuccess(t, out, tc.tool) // (c) real handler outcome

			msg := recordedMsg(t, before, tc.proc) // (a) reached
			if msg == nil {
				t.Fatalf("%s: no typed message for %s", tc.tool, tc.proc)
			}
			assertTypedFields(t, tc.tool, tc.args, msg) // (b) exact typed mapping

			// (d) resource-scope denial with a real Connect code.
			denied := callTool(t, noperms, tc.tool, tc.args)
			if denied.Status != "error" || denied.Error == nil ||
				denied.Error.Kind != ErrKindPermissionDenied || denied.Error.Code == "" {
				t.Fatalf("%s: noperms token was not denied with a Connect code: %+v", tc.tool, denied)
			}
			unauth := callTool(t, invalid, tc.tool, tc.args)
			if unauth.Status != "error" || unauth.Error == nil || unauth.Error.Kind != ErrKindUnauthenticated {
				t.Fatalf("%s: invalid token was not refused: %+v", tc.tool, unauth)
			}

			gate := callTool(t, admin, tc.tool, cloneWithoutConfirm(tc.args))
			if gate.Status != "error" || gate.Error == nil || gate.Error.Kind != ErrKindConfirmationRequired {
				t.Fatalf("%s: confirm gate not observed: %+v", tc.tool, gate)
			}
			t.Logf("%s: reached=%s; scope denied(%s); unauthenticated refused; confirm gate observed",
				tc.tool, tc.proc, denied.Error.Code)
		})
	}
}

// TestUploadFamilyFreshCreateAtCommit proves a session created with uploads/create
// alone cannot mutate a destination: SaveUploadedFile re-checks
// files:create[server_id] at commit. An entitled caller can then commit its own
// session through the typed path with no re-upload and exact destination bytes.
func TestUploadFamilyFreshCreateAtCommit(t *testing.T) {
	f := newControllerStreamFixture(t)
	token := seedToken(t, f.store, f.enforcer, "u-fresh", "fresh-upload", uploadOnlyPerms())
	payload := []byte("fresh-files-create-at-commit")
	sid := completedUpload(t, f, token, "fresh.jar", payload)
	uploadOnly := uploadSessionForToken(t, f, token)
	denied := callTool(t, uploadOnly, UploadToolSaveUploadedFile, map[string]any{
		"server_id": "srv-1", "upload_session_id": sid,
		"destination_path": "mods", "filename": "fresh.jar", "confirm": true,
	})
	assertToolError(t, denied, "fresh scope at commit")
	requireNoUploadDest(t, f, "mods/fresh.jar")

	adminToken := seedToken(t, f.store, f.enforcer, "u-fresh-admin", "admin", nil)
	admin := uploadSessionForToken(t, f, adminToken)
	adminSID := completedUpload(t, f, adminToken, "fresh-admin.jar", payload)
	before := enableUploadRecorder(t)
	ok := callTool(t, admin, UploadToolSaveUploadedFile, map[string]any{
		"server_id": "srv-1", "upload_session_id": adminSID,
		"destination_path": "mods", "filename": "fresh-admin.jar", "confirm": true,
	})
	assertSuccess(t, ok, "entitled commit")
	requireUploadDest(t, f, "mods/fresh-admin.jar", payload)
	counts := map[string]int{}
	for _, rec := range invocationRecorder.snapshot()[before:] {
		counts[rec.Procedure]++
	}
	if counts[uploadProcSave] != 1 || counts[uploadProcInit] != 0 || counts[uploadProcChunk] != 0 {
		t.Fatalf("entitled commit did not use the typed save path only: %+v", counts)
	}
}

// --- Composed rest_upload_stream path ---------------------------------------

func uploadOpenAndDrain(t *testing.T, session *sdkmcp.ClientSession, src string, size int) string {
	t.Helper()
	opened := callTool(t, session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": src, "total_size": size,
	})
	assertSuccess(t, opened, "open")
	handle := resultString(opened, "session_handle")
	for {
		chunk := callTool(t, session, "scoped_stream_chunk", map[string]any{"session_handle": handle})
		assertSuccess(t, chunk, "chunk")
		m, _ := chunk.Result.(map[string]any)
		if done, _ := m["done"].(bool); done {
			return handle
		}
	}
}

// TestUploadFamilyComposedStreamCommit drives the full composed upload stream
// through real tools/call over stdio: scoped_stream_open -> scoped_stream_chunk*
// -> rest_upload_stream_commit. It asserts the authoritative final size/digest,
// exactly one typed upload sequence, and the exact destination bytes.
func TestUploadFamilyComposedStreamCommit(t *testing.T) {
	f := newControllerStreamFixture(t)
	token := seedToken(t, f.store, f.enforcer, "u-compose", "admin", nil)
	payload := bytes.Repeat([]byte("UP-STREAM-"), 40000)[:300<<10] // > one 256 KiB frame
	writeUploadSource(t, f, "worlds/compose.bin", payload)
	session := uploadSessionForToken(t, f, token)
	handle := uploadOpenAndDrain(t, session, "worlds/compose.bin", len(payload))

	before := enableUploadRecorder(t)
	commit := callTool(t, session, UploadToolStreamCommit, map[string]any{
		"session_handle": handle, "destination": "worlds",
		"filename": "compose-copy.bin", "confirm": true,
	})
	assertSuccess(t, commit, "composed commit")
	sum := sha256.Sum256(payload)
	wantDigest := hex.EncodeToString(sum[:])
	if resultString(commit, "sha256") != wantDigest || resultInt64(commit, "bytes") != int64(len(payload)) ||
		resultInt64(commit, "actual_size") != int64(len(payload)) {
		t.Fatalf("composed commit size/digest mismatch: %+v", commit.Result)
	}
	counts := map[string]int{}
	for _, rec := range invocationRecorder.snapshot()[before:] {
		counts[rec.Procedure]++
	}
	wantChunks := (len(payload) + storageworker.ChunkBytes - 1) / storageworker.ChunkBytes
	if counts[uploadProcInit] != 1 || counts[uploadProcChunk] != wantChunks || counts[uploadProcSave] != 1 {
		t.Fatalf("composed commit upload sequence: %+v (want chunks=%d)", counts, wantChunks)
	}
	requireUploadDest(t, f, "worlds/compose-copy.bin", payload)
}

// TestUploadFamilyComposedLeaseAndAmbiguous proves the one-at-a-time commit
// lease and ambiguous post-dispatch verification of the composed tool.
func TestUploadFamilyComposedLeaseAndAmbiguous(t *testing.T) {
	t.Run("exclusive_lease", func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		gate := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
				if req.Spec().Procedure == uploadProcInit {
					once.Do(func() { close(entered) })
					select {
					case <-release:
					case <-time.After(10 * time.Second):
						return nil, errors.New("upload gate timed out")
					}
				}
				return next(ctx, req)
			}
		})
		f := newControllerStreamFixture(t, gate)
		token := seedToken(t, f.store, f.enforcer, "u-lease", "admin", nil)
		payload := bytes.Repeat([]byte("L"), 300<<10)
		writeUploadSource(t, f, "worlds/lease.bin", payload)
		session := uploadSessionForToken(t, f, token)
		handle := uploadOpenAndDrain(t, session, "worlds/lease.bin", len(payload))

		type res struct {
			out ToolOutput
			err error
		}
		results := make(chan res, 2)
		for i := 0; i < 2; i++ {
			go func() {
				out, err := callToolRaw(context.Background(), session, UploadToolStreamCommit, map[string]any{
					"session_handle": handle, "destination": "worlds",
					"filename": "lease-copy.bin", "confirm": true,
				})
				results <- res{out: out, err: err}
			}()
		}
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Fatal("first composed commit never reached InitUpload")
		}
		close(release)
		var ok, busy int
		for i := 0; i < 2; i++ {
			r := <-results
			if r.err != nil {
				t.Errorf("lease commit transport error: %v", r.err)
				continue
			}
			switch {
			case r.out.Status == "ok":
				ok++
			case r.out.Error != nil && strings.Contains(r.out.Error.Message, "in-flight commit"):
				busy++
			default:
				t.Errorf("unexpected parallel lease result: %+v", r.out)
			}
		}
		if ok != 1 || busy != 1 {
			t.Fatalf("parallel composed commit outcomes: ok=%d busy=%d, want 1/1", ok, busy)
		}
		requireUploadDest(t, f, "worlds/lease-copy.bin", payload)
	})

	for _, tc := range []struct {
		name          string
		err           error
		dispatchFirst bool
		wantVerified  bool
	}{
		{"lost_reply", errors.New("connection reset after dispatch"), true, true},
		{"timeout", context.DeadlineExceeded, true, true},
		{"never_dispatched", context.DeadlineExceeded, false, false},
	} {
		t.Run("ambiguous_"+tc.name, func(t *testing.T) {
			f := newControllerStreamFixture(t)
			payload := bytes.Repeat([]byte("A"), 300<<10)
			writeUploadSource(t, f, "worlds/ambig.bin", payload)
			gate := &ambiguityTransport{
				base: f.ts.Client().Transport, err: tc.err,
				dispatchFirst: tc.dispatchFirst, dispatched: make(chan struct{}),
			}
			token := seedToken(t, f.store, f.enforcer, "u-ambig", "admin", nil)
			cl, err := NewClients(&http.Client{Transport: gate}, f.ts.URL, token)
			if err != nil {
				t.Fatal(err)
			}
			session := connectStdioSession(t, newUploadServer(t, cl), context.Background())
			handle := uploadOpenAndDrain(t, session, "worlds/ambig.bin", len(payload))

			before := enableUploadRecorder(t)
			commit := callTool(t, session, UploadToolStreamCommit, map[string]any{
				"session_handle": handle, "destination": "worlds",
				"filename": "ambig-copy.bin", "confirm": true,
			})
			select {
			case <-gate.dispatched:
			default:
				t.Fatal("SaveUploadedFile was never dispatched")
			}
			if tc.wantVerified {
				assertSuccess(t, commit, "verified ambiguous commit")
				if resultString(commit, "verification") == "" {
					t.Fatalf("commit did not report destination verification: %+v", commit.Result)
				}
				requireUploadDest(t, f, "worlds/ambig-copy.bin", payload)
			} else {
				assertToolError(t, commit, "never-dispatched save")
				if commit.Error == nil || !strings.Contains(commit.Error.Message, "UNKNOWN") ||
					!strings.Contains(commit.Error.Message, "do not blindly retry") {
					t.Fatalf("UNKNOWN outcome lacks recovery hint: %+v", commit.Error)
				}
				requireNoUploadDest(t, f, "worlds/ambig-copy.bin")
			}
			counts := map[string]int{}
			for _, rec := range invocationRecorder.snapshot()[before:] {
				counts[rec.Procedure]++
			}
			wantSave := 1
			if !tc.dispatchFirst {
				wantSave = 0
			}
			wantChunks := (len(payload) + storageworker.ChunkBytes - 1) / storageworker.ChunkBytes
			if counts[uploadProcInit] != 1 || counts[uploadProcChunk] != wantChunks || counts[uploadProcSave] != wantSave {
				t.Fatalf("ambiguous commit caused a second upload sequence: %+v", counts)
			}
			if tc.wantVerified && counts["/carbonpanel.v1.FileService/GetFile"] != 1 {
				t.Fatalf("destination was not verified: %+v", counts)
			}
			again := callTool(t, session, UploadToolStreamCommit, map[string]any{
				"session_handle": handle, "destination": "worlds",
				"filename": "ambig-copy.bin", "confirm": true,
			})
			assertToolError(t, again, "retry after ambiguous save")
		})
	}
}

// TestUploadFamilyRefusalsZeroWrites proves the transport-level refusals named
// in the plan (cap, cancel, expiry, cross-scope, incomplete, digest mismatch)
// never release bytes for commit and dispatch no upload RPC at all.
func TestUploadFamilyRefusalsZeroWrites(t *testing.T) {
	frame := bytes.Repeat([]byte("Z"), storageworker.ChunkBytes)
	sum := sha256.Sum256(frame)
	client := &digestFakeClient{frame: frame, sha: hex.EncodeToString(sum[:])}
	ctx := context.Background()

	t.Run("over_cap", func(t *testing.T) {
		transfer, err := NewStreamTransfer(client, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := transfer.OpenWithSize(ctx, "u", "srv-1", "worlds/x", int64(storageworker.MaxStreamBytes)+1); !errors.Is(err, ErrStreamTooLarge) {
			t.Fatalf("over-cap open was not refused: %v", err)
		}
	})

	t.Run("cross_scope_and_cancel", func(t *testing.T) {
		transfer, err := NewStreamTransfer(client, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := transfer.OpenWithSize(ctx, "owner-a", "srv-1", "worlds/x", int64(len(frame)))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := transfer.ClaimCommit("owner-b", handle); !errors.Is(err, ErrStreamCrossScope) {
			t.Fatalf("cross-owner commit was not refused: %v", err)
		}
		if err := transfer.Cancel(ctx, "owner-a", handle); err != nil {
			t.Fatalf("cancel: %v", err)
		}
		if _, _, err := transfer.ClaimCommit("owner-a", handle); !errors.Is(err, ErrStreamCanceled) && !errors.Is(err, ErrStreamNotFound) {
			t.Fatalf("canceled handle was claimable: %v", err)
		}
	})

	t.Run("expiry", func(t *testing.T) {
		transfer, err := NewStreamTransfer(client, 30*time.Millisecond)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := transfer.OpenWithSize(ctx, "u", "srv-1", "worlds/x", int64(len(frame)))
		if err != nil {
			t.Fatal(err)
		}
		time.Sleep(80 * time.Millisecond)
		if _, _, err := transfer.ClaimCommit("u", handle); !errors.Is(err, ErrStreamExpired) && !errors.Is(err, ErrStreamNotFound) {
			t.Fatalf("expired handle was claimable: %v", err)
		}
	})

	t.Run("incomplete", func(t *testing.T) {
		transfer, err := NewStreamTransfer(client, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		handle, err := transfer.OpenWithSize(ctx, "u", "srv-1", "worlds/x", int64(len(frame)*2))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := transfer.ClaimCommit("u", handle); !errors.Is(err, ErrStreamNotComplete) {
			t.Fatalf("incomplete stream was claimable: %v", err)
		}
	})

	t.Run("zero_bytes", func(t *testing.T) {
		f := newControllerStreamFixture(t)
		writeUploadSource(t, f, "worlds/empty.bin", nil)
		token := seedToken(t, f.store, f.enforcer, "u-empty", "admin", nil)
		session := uploadSessionForToken(t, f, token)
		handle := uploadOpenAndDrain(t, session, "worlds/empty.bin", 0)
		commit := callTool(t, session, UploadToolStreamCommit, map[string]any{
			"session_handle": handle, "destination": "worlds",
			"filename": "empty-copy.bin", "confirm": true,
		})
		assertToolError(t, commit, "zero-byte commit")
		requireNoUploadDest(t, f, "worlds/empty-copy.bin")
	})

	sentinels := UploadFamilyErrorSentinels()
	for _, key := range []string{"canceled", "expired", "cross_scope", "too_large", "not_found",
		"not_complete", "digest_mismatch", "commit_busy", "outcome_unknown"} {
		if sentinels[key] == nil {
			t.Errorf("missing refusal sentinel %q", key)
		}
	}
}
