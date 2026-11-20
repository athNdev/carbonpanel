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
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1/carbonpanelv1connect"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

// --- Early / mismatched commit refusal -------------------------------------

// TestIntegrationControllerStreamEarlyCommitIsRefused proves a partially read
// stream cannot reach the upload/commit path: no InitUpload or SaveUploadedFile
// RPC is sent and no destination bytes are written.
func TestIntegrationControllerStreamEarlyCommitIsRefused(t *testing.T) {
	f := newControllerStreamFixture(t)
	acc, err := nodestorage.NewLocal(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = acc.Close() }()
	payload := bytes.Repeat([]byte("e"), 300<<10)
	if err := acc.WriteFile("worlds/early.bin", payload); err != nil {
		t.Fatal(err)
	}

	session := f.session(t, "u-early-commit", "admin")
	invocationRecorder.enable()
	defer invocationRecorder.disable()
	before := len(invocationRecorder.snapshot())

	opened := callTool(t, session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/early.bin", "total_size": len(payload),
	})
	assertSuccess(t, opened, "scoped_stream_open")
	handle := resultString(opened, "session_handle")
	// Read exactly one frame; a 300 KiB source needs two frames, so the stream
	// is deliberately NOT complete at commit time.
	assertSuccess(t, callTool(t, session, "scoped_stream_chunk", map[string]any{"session_handle": handle}),
		"first chunk")
	commit := callTool(t, session, "scoped_stream_commit", map[string]any{
		"session_handle": handle, "destination": "worlds", "filename": "early-copy.bin", "confirm": true,
	})
	assertToolError(t, commit, "early commit")
	if commit.Error == nil || !strings.Contains(commit.Error.Message, "not complete") {
		t.Fatalf("early commit error does not identify incomplete stream: %+v", commit.Error)
	}
	for _, rec := range invocationRecorder.snapshot()[before:] {
		if rec.Procedure == "/carbonpanel.v1.UploadService/InitUpload" ||
			rec.Procedure == "/carbonpanel.v1.FileService/SaveUploadedFile" {
			t.Fatalf("early commit reached controller RPC %s", rec.Procedure)
		}
	}
	if _, err := os.Stat(filepath.Join(f.root, "worlds", "early-copy.bin")); !os.IsNotExist(err) {
		t.Fatalf("early commit wrote a destination file: %v", err)
	}
}

// digestFakeClient returns a fixed final frame so gateway digest validation
// can be tested without a real controller. Only the three stream methods are
// used; the embedded interface supplies the rest. onOpen can force a post-Open
// admission race; closes records the controller close and the context seen by
// the fake so terminal cleanup can be asserted.
type digestFakeClient struct {
	carbonpanelv1connect.FileServiceClient
	frame  []byte
	sha    string
	onOpen func()
	closes []streamCloseRecord
}

type streamCloseRecord struct {
	sessionID string
	cancel    bool
	ctxErr    error
}

func (c *digestFakeClient) OpenStream(_ context.Context,
	req *connect.Request[v1.OpenStreamRequest]) (*connect.Response[v1.OpenStreamResponse], error) {
	if c.onOpen != nil {
		c.onOpen()
	}
	return connect.NewResponse(&v1.OpenStreamResponse{SessionId: "ctl-fake", Size: req.Msg.GetTotalSize()}), nil
}

func (c *digestFakeClient) ReadStreamFrame(_ context.Context,
	_ *connect.Request[v1.ReadStreamFrameRequest]) (*connect.Response[v1.ReadStreamFrameResponse], error) {
	return connect.NewResponse(&v1.ReadStreamFrameResponse{
		Data: c.frame, Done: true, Offset: int64(len(c.frame)), Sha256: c.sha,
	}), nil
}

func (c *digestFakeClient) CloseStream(ctx context.Context,
	req *connect.Request[v1.CloseStreamRequest]) (*connect.Response[v1.CloseStreamResponse], error) {
	c.closes = append(c.closes, streamCloseRecord{
		sessionID: req.Msg.GetSessionId(),
		cancel:    req.Msg.GetCancel(),
		ctxErr:    ctx.Err(),
	})
	return connect.NewResponse(&v1.CloseStreamResponse{}), nil
}

// TestGatewayTerminalFailureClosesControllerHandleAndRecovers proves the two
// terminal gateway-side failure paths close the controller-owned handle with a
// context independent of an already-canceled tools/call, release local staging,
// and leave the registry able to accept a fresh successful transfer.
func TestGatewayTerminalFailureClosesControllerHandleAndRecovers(t *testing.T) {
	frame := bytes.Repeat([]byte("z"), storageworker.ChunkBytes)
	sum := sha256.Sum256(frame)
	goodSHA := hex.EncodeToString(sum[:])
	wrong := sha256.Sum256([]byte("not-the-frame"))
	wrongSHA := hex.EncodeToString(wrong[:])

	t.Run("open_admission_rejection", func(t *testing.T) {
		var transfer *StreamTransfer
		client := &digestFakeClient{frame: frame, sha: goodSHA}
		client.onOpen = func() {
			// The pre-RPC admission check saw an empty registry; fill it during
			// OpenStream so the post-RPC re-check refuses the owned handle.
			transfer.mu.Lock()
			defer transfer.mu.Unlock()
			for i := 0; i < maxGatewaySessions; i++ {
				transfer.sessions[fmt.Sprintf("dummy-%d", i)] = &streamSession{}
			}
		}
		var err error
		transfer, err = NewStreamTransfer(client, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, err := transfer.OpenWithSize(ctx, "u-open", "srv-1", "worlds/x", int64(len(frame)));
			!errors.Is(err, ErrStreamAdmission) {
			t.Fatalf("post-Open admission was not refused: %v", err)
		}
		if len(client.closes) != 1 {
			t.Fatalf("controller close records: got %d want 1", len(client.closes))
		}
		gotClose := client.closes[0]
		if gotClose.sessionID != "ctl-fake" || !gotClose.cancel || gotClose.ctxErr != nil {
			t.Fatalf("terminal open cleanup: %+v", gotClose)
		}
		transfer.mu.Lock()
		count, staged := len(transfer.sessions), transfer.aggregate
		transfer.mu.Unlock()
		if count != maxGatewaySessions || staged != 0 {
			t.Fatalf("admission cleanup changed registry: sessions=%d staged=%d", count, staged)
		}
		for i := 0; i < maxGatewaySessions; i++ {
			transfer.Release("u-open", fmt.Sprintf("dummy-%d", i))
		}
	})

	t.Run("terminal_read_failure_then_recovery", func(t *testing.T) {
		client := &digestFakeClient{frame: frame, sha: wrongSHA}
		transfer, err := NewStreamTransfer(client, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		handle, err := transfer.OpenWithSize(ctx, "u-read", "srv-1", "worlds/x", int64(len(frame)))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := transfer.ReadChunk(ctx, "u-read", handle); !errors.Is(err, ErrStreamDigestMismatch) {
			t.Fatalf("terminal read failure was not refused: %v", err)
		}
		if len(client.closes) != 1 {
			t.Fatalf("controller close records: got %d want 1", len(client.closes))
		}
		gotClose := client.closes[0]
		if gotClose.sessionID != "ctl-fake" || !gotClose.cancel || gotClose.ctxErr != nil {
			t.Fatalf("terminal read cleanup: %+v", gotClose)
		}
		if _, _, err := transfer.ClaimCommit("u-read", handle); !errors.Is(err, ErrStreamNotFound) {
			t.Fatalf("failed session still holds staged bytes: %v", err)
		}
		transfer.mu.Lock()
		count, staged := len(transfer.sessions), transfer.aggregate
		transfer.mu.Unlock()
		if count != 0 || staged != 0 {
			t.Fatalf("terminal cleanup left state: sessions=%d staged=%d", count, staged)
		}

		// A fresh session with a valid digest still completes normally.
		goodClient := &digestFakeClient{frame: frame, sha: goodSHA}
		goodTransfer, err := NewStreamTransfer(goodClient, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		goodHandle, err := goodTransfer.OpenWithSize(context.Background(), "u-read", "srv-1", "worlds/x", int64(len(frame)))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := goodTransfer.ReadChunk(context.Background(), "u-read", goodHandle); !errors.Is(err, io.EOF) {
			t.Fatalf("fresh read did not complete: %v", err)
		}
		data, digest, err := goodTransfer.ClaimCommit("u-read", goodHandle)
		if err != nil || digest != goodSHA || !bytes.Equal(data, frame) {
			t.Fatalf("fresh commit failed: digest=%q bytes=%d err=%v", digest, len(data), err)
		}
		goodTransfer.FinishCommit("u-read", goodHandle, CommitSucceeded)
	})
}

// TestGatewayRejectsMismatchedControllerDigest proves the gateway refuses a
// final frame whose controller SHA does not match the locally computed digest,
// and that Frames therefore cannot release such bytes for commit.
func TestGatewayRejectsMismatchedControllerDigest(t *testing.T) {
	frame := bytes.Repeat([]byte("d"), storageworker.ChunkBytes)
	wrong := sha256.Sum256([]byte("not-the-frame"))
	client := &digestFakeClient{frame: frame, sha: hex.EncodeToString(wrong[:])}
	transfer, err := NewStreamTransfer(client, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	handle, err := transfer.OpenWithSize(ctx, "u-digest", "srv-1", "worlds/x.bin", int64(len(frame)))
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = transfer.ReadChunk(ctx, "u-digest", handle)
	if !errors.Is(err, ErrStreamDigestMismatch) {
		t.Fatalf("mismatched digest was not refused: %v", err)
	}
	if _, _, err := transfer.ClaimCommit("u-digest", handle); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("failed session still holds staged bytes: %v", err)
	}
}

// --- Parallel tools/call ----------------------------------------------------

func callToolRaw(ctx context.Context, session *sdkmcp.ClientSession, name string, args map[string]any) (ToolOutput, error) {
	res, err := session.CallTool(ctx, &sdkmcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		return ToolOutput{}, err
	}
	if res.StructuredContent == nil {
		return ToolOutput{}, errors.New("no structured content")
	}
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return ToolOutput{}, err
	}
	var out ToolOutput
	if err := json.Unmarshal(raw, &out); err != nil {
		return ToolOutput{}, err
	}
	return out, nil
}

// TestIntegrationControllerStreamParallelToolsCall drives several independent
// streaming workflows concurrently over real MCP sessions against the real
// authenticated controller. Under the race detector this proves the gateway's
// per-session serialization and offset/SHA accounting are race-free.
func TestIntegrationControllerStreamParallelToolsCall(t *testing.T) {
	f := newControllerStreamFixture(t)
	acc, err := nodestorage.NewLocal(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = acc.Close() }()
	const streams = 4
	payloads := make([][]byte, streams)
	for i := range payloads {
		payloads[i] = bytes.Repeat([]byte{byte('a' + i)}, 300<<10+i*17)
		if err := acc.WriteFile(filepath.Join("worlds", "parallel-"+string(rune('a'+i))+".bin"), payloads[i]); err != nil {
			t.Fatal(err)
		}
	}
	sessions := make([]*sdkmcp.ClientSession, streams)
	for i := range sessions {
		sessions[i] = f.session(t, "u-parallel-"+string(rune('a'+i)), "admin")
	}

	type result struct {
		index int
		err   error
		sha   string
		bytes int64
	}
	out := make(chan result, streams)
	var wg sync.WaitGroup
	for i := 0; i < streams; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "worlds/parallel-" + string(rune('a'+i)) + ".bin"
			ctx := context.Background()
			opened, err := callToolRaw(ctx, sessions[i], "scoped_stream_open", map[string]any{
				"server_id": "srv-1", "path": name, "total_size": len(payloads[i]),
			})
			if err != nil {
				out <- result{index: i, err: err}
				return
			}
			handle := resultString(opened, "session_handle")
			got := []byte{}
			digest := ""
			for {
				chunk, err := callToolRaw(ctx, sessions[i], "scoped_stream_chunk", map[string]any{"session_handle": handle})
				if err != nil {
					out <- result{index: i, err: err}
					return
				}
				raw, err := base64.StdEncoding.DecodeString(resultString(chunk, "data"))
				if err != nil {
					out <- result{index: i, err: err}
					return
				}
				got = append(got, raw...)
				m, _ := chunk.Result.(map[string]any)
				if done, _ := m["done"].(bool); done {
					digest = resultString(chunk, "sha256")
					break
				}
			}
			if !bytes.Equal(got, payloads[i]) {
				out <- result{index: i, err: errors.New("payload mismatch")}
				return
			}
			commit, err := callToolRaw(ctx, sessions[i], "scoped_stream_commit", map[string]any{
				"session_handle": handle, "destination": "worlds",
				"filename": "parallel-copy-" + string(rune('a'+i)) + ".bin", "confirm": true,
			})
			if err != nil {
				out <- result{index: i, err: err}
				return
			}
			out <- result{index: i, sha: digest, bytes: resultInt64(commit, "bytes")}
		}(i)
	}
	wg.Wait()
	close(out)
	for r := range out {
		if r.err != nil {
			t.Errorf("parallel stream %d failed: %v", r.index, r.err)
			continue
		}
		sum := sha256.Sum256(payloads[r.index])
		if r.sha != hex.EncodeToString(sum[:]) || r.bytes != int64(len(payloads[r.index])) {
			t.Errorf("parallel stream %d digest/size mismatch: %s %d", r.index, r.sha, r.bytes)
		}
	}
}

// --- Gateway memory / admission --------------------------------------------

// TestGatewayStreamMemoryIsBounded proves expired and canceled sessions release
// staged bytes and registry entries, and that admission is capped.
func TestGatewayStreamMemoryIsBounded(t *testing.T) {
	frame := bytes.Repeat([]byte("m"), storageworker.ChunkBytes)
	sum := sha256.Sum256(frame)
	client := &digestFakeClient{frame: frame, sha: hex.EncodeToString(sum[:])}
	transfer, err := NewStreamTransfer(client, 60*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// Admission is bounded independently of TTL.
	longTransfer, err := NewStreamTransfer(client, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	handles := make([]string, 0, maxGatewaySessions)
	for i := 0; i < maxGatewaySessions; i++ {
		h, err := longTransfer.OpenWithSize(ctx, "u-cap", "srv-1", "worlds/cap.bin", int64(len(frame)))
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		handles = append(handles, h)
	}
	if _, err := longTransfer.OpenWithSize(ctx, "u-cap", "srv-1", "worlds/cap.bin", int64(len(frame)));
		!errors.Is(err, ErrStreamAdmission) {
		t.Fatalf("admission cap not enforced: %v", err)
	}
	for _, h := range handles {
		longTransfer.Release("u-cap", h)
	}

	// Stage large frames in many sessions, then cancel half and let the rest
	// expire. Both paths must return aggregate memory to zero.
	const sessions = 32
	short := make([]string, 0, sessions)
	for i := 0; i < sessions; i++ {
		h, err := transfer.OpenWithSize(ctx, "u-memory", "srv-1", "worlds/mem.bin", int64(len(frame)))
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		short = append(short, h)
		if _, _, _, err := transfer.ReadChunk(ctx, "u-memory", h); err != nil && !errors.Is(err, io.EOF) {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	transfer.mu.Lock()
	staged := transfer.aggregate
	count := len(transfer.sessions)
	transfer.mu.Unlock()
	if staged != sessions*len(frame) || count != sessions {
		t.Fatalf("staging state: bytes=%d sessions=%d", staged, count)
	}
	for _, h := range short[:sessions/2] {
		if err := transfer.Cancel(ctx, "u-memory", h); err != nil {
			t.Fatalf("cancel: %v", err)
		}
	}
	transfer.mu.Lock()
	afterCancel := transfer.aggregate
	transfer.mu.Unlock()
	if afterCancel != (sessions/2)*len(frame) {
		t.Fatalf("cancel did not free staged bytes: %d", afterCancel)
	}
	time.Sleep(250 * time.Millisecond) // TTL timers for the uncanceled half
	transfer.mu.Lock()
	finalBytes, finalCount := transfer.aggregate, len(transfer.sessions)
	transfer.mu.Unlock()
	if finalBytes != 0 || finalCount != 0 {
		t.Fatalf("TTL did not reclaim gateway state: bytes=%d sessions=%d", finalBytes, finalCount)
	}
}

// --- One-at-a-time commit lease ---------------------------------------------

// TestGatewayCommitLeaseIsExclusive proves two concurrent claims of one handle
// cannot both start an upload: the loser gets a busy result and holds no bytes.
func TestGatewayCommitLeaseIsExclusive(t *testing.T) {
	frame := bytes.Repeat([]byte("L"), storageworker.ChunkBytes)
	sum := sha256.Sum256(frame)
	client := &digestFakeClient{frame: frame, sha: hex.EncodeToString(sum[:])}
	transfer, err := NewStreamTransfer(client, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	handle, err := transfer.OpenWithSize(ctx, "u-lease", "srv-1", "worlds/lease.bin", int64(len(frame)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := transfer.ReadChunk(ctx, "u-lease", handle); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}

	type claim struct {
		data []byte
		err  error
	}
	first := make(chan claim, 1)
	go func() {
		data, _, err := transfer.ClaimCommit("u-lease", handle)
		first <- claim{data: data, err: err}
	}()
	got := <-first
	if got.err != nil || len(got.data) != len(frame) {
		t.Fatalf("first claim failed: %v", got.err)
	}
	if _, _, err := transfer.ClaimCommit("u-lease", handle); !errors.Is(err, ErrStreamCommitBusy) {
		t.Fatalf("parallel claim was not refused as busy: %v", err)
	}
	transfer.FinishCommit("u-lease", handle, CommitSucceeded)
	transfer.mu.Lock()
	bytesLeft, sessions := transfer.aggregate, len(transfer.sessions)
	transfer.mu.Unlock()
	if bytesLeft != 0 || sessions != 0 {
		t.Fatalf("successful commit did not free budget: bytes=%d sessions=%d", bytesLeft, sessions)
	}
	// Idempotent finish must not double-free or resurrect a claim.
	transfer.FinishCommit("u-lease", handle, CommitSucceeded)
	if _, _, err := transfer.ClaimCommit("u-lease", handle); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("committed handle is still claimable: %v", err)
	}
}

// TestGatewayCommitLeasePinsBudgetDuringTTLAndCancel proves TTL/cancel cannot
// free gateway budget while an in-flight commit still references the slice,
// and that FinishCommit performs exactly one terminal release.
func TestGatewayCommitLeasePinsBudgetDuringTTLAndCancel(t *testing.T) {
	frame := bytes.Repeat([]byte("P"), storageworker.ChunkBytes)
	sum := sha256.Sum256(frame)
	client := &digestFakeClient{frame: frame, sha: hex.EncodeToString(sum[:])}
	transfer, err := NewStreamTransfer(client, 40*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	handle, err := transfer.OpenWithSize(ctx, "u-pin", "srv-1", "worlds/pin.bin", int64(len(frame)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := transfer.ReadChunk(ctx, "u-pin", handle); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	held, _, err := transfer.ClaimCommit("u-pin", handle)
	if err != nil || len(held) != len(frame) {
		t.Fatalf("claim failed: %v", err)
	}

	// TTL elapses and cancel arrives while the uploader still holds `held`.
	time.Sleep(100 * time.Millisecond)
	if err := transfer.Cancel(ctx, "u-pin", handle); err != nil && !errors.Is(err, ErrStreamCanceled) {
		t.Fatalf("cancel during commit: %v", err)
	}
	transfer.mu.Lock()
	pinnedBytes, sessions := transfer.aggregate, len(transfer.sessions)
	transfer.mu.Unlock()
	if pinnedBytes != len(frame) || sessions != 1 {
		t.Fatalf("TTL/cancel freed pinned budget: bytes=%d sessions=%d", pinnedBytes, sessions)
	}
	// The uploader's slice is still intact and its digest still validates.
	if !bytes.Equal(held, frame) {
		t.Fatal("in-flight commit slice was mutated by TTL/cancel")
	}
	transfer.FinishCommit("u-pin", handle, CommitRetryable) // terminal: canceled
	transfer.FinishCommit("u-pin", handle, CommitRetryable) // idempotent
	transfer.mu.Lock()
	finalBytes, finalSessions, pins := transfer.aggregate, len(transfer.sessions), len(transfer.pinned)
	transfer.mu.Unlock()
	if finalBytes != 0 || finalSessions != 0 || pins != 0 {
		t.Fatalf("terminal release leaked state: bytes=%d sessions=%d pins=%d", finalBytes, finalSessions, pins)
	}
	if _, _, err := transfer.ClaimCommit("u-pin", handle); !errors.Is(err, ErrStreamNotFound) {
		t.Fatalf("terminated handle is still claimable: %v", err)
	}
}

// TestIntegrationControllerStreamParallelCommitOfOneHandle drives two real
// tools/call commit requests for the SAME handle. The upload interceptor holds
// the first one in InitUpload so the second must observe the busy lease before
// any second mutation. Exactly one upload sequence reaches the controller.
func TestIntegrationControllerStreamParallelCommitOfOneHandle(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	blockUpload := connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if req.Spec().Procedure == "/carbonpanel.v1.UploadService/InitUpload" {
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
	f := newControllerStreamFixture(t, blockUpload)
	acc, err := nodestorage.NewLocal(f.root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = acc.Close() }()
	payload := bytes.Repeat([]byte("C"), 300<<10)
	if err := acc.WriteFile("worlds/once.bin", payload); err != nil {
		t.Fatal(err)
	}
	session := f.session(t, "u-once", "admin")
	opened := callTool(t, session, "scoped_stream_open", map[string]any{
		"server_id": "srv-1", "path": "worlds/once.bin", "total_size": len(payload),
	})
	handle := resultString(opened, "session_handle")
	for {
		out := callTool(t, session, "scoped_stream_chunk", map[string]any{"session_handle": handle})
		assertSuccess(t, out, "chunk")
		m, _ := out.Result.(map[string]any)
		if done, _ := m["done"].(bool); done {
			break
		}
	}

	invocationRecorder.enable()
	defer invocationRecorder.disable()
	before := len(invocationRecorder.snapshot())
	type res struct {
		out ToolOutput
		err error
	}
	results := make(chan res, 2)
	for i := 0; i < 2; i++ {
		go func(n int) {
			out, err := callToolRaw(context.Background(), session, "scoped_stream_commit", map[string]any{
				"session_handle": handle, "destination": "worlds",
				"filename": "once-copy.bin", "confirm": true,
			})
			results <- res{out: out, err: err}
		}(i)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first commit never reached InitUpload")
	}
	close(release)
	var ok, busy int
	for i := 0; i < 2; i++ {
		r := <-results
		if r.err != nil {
			t.Errorf("commit transport error: %v", r.err)
			continue
		}
		switch {
		case r.out.Status == "ok":
			ok++
		case r.out.Error != nil && strings.Contains(r.out.Error.Message, "in-flight commit"):
			busy++
		default:
			t.Errorf("unexpected parallel commit result: %+v", r.out)
		}
	}
	if ok != 1 || busy != 1 {
		t.Fatalf("parallel commit outcomes: ok=%d busy=%d, want 1/1", ok, busy)
	}
	counts := map[string]int{}
	for _, rec := range invocationRecorder.snapshot()[before:] {
		counts[rec.Procedure]++
	}
	wantChunks := (len(payload) + storageworker.ChunkBytes - 1) / storageworker.ChunkBytes
	expected := map[string]int{
		"/carbonpanel.v1.UploadService/InitUpload":      1,
		"/carbonpanel.v1.UploadService/UploadChunk":     wantChunks,
		"/carbonpanel.v1.FileService/SaveUploadedFile":  1,
	}
	for proc, want := range expected {
		if counts[proc] != want {
			t.Errorf("controller RPC %s called %d times, want %d (exactly one commit sequence)", proc, counts[proc], want)
		}
	}
}

// TestGatewayCommitFailureIsRetryable proves an ordinary upload failure
// releases the lease without dropping bytes, so the same handle can be
// retried deterministically with identical data and digest.
func TestGatewayCommitFailureIsRetryable(t *testing.T) {
	frame := bytes.Repeat([]byte("R"), storageworker.ChunkBytes)
	sum := sha256.Sum256(frame)
	client := &digestFakeClient{frame: frame, sha: hex.EncodeToString(sum[:])}
	transfer, err := NewStreamTransfer(client, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	handle, err := transfer.OpenWithSize(ctx, "u-retry", "srv-1", "worlds/retry.bin", int64(len(frame)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := transfer.ReadChunk(ctx, "u-retry", handle); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	first, digest, err := transfer.ClaimCommit("u-retry", handle)
	if err != nil {
		t.Fatal(err)
	}
	transfer.FinishCommit("u-retry", handle, CommitRetryable) // simulated upload failure
	transfer.mu.Lock()
	bytesAfterFailure := transfer.aggregate
	transfer.mu.Unlock()
	if bytesAfterFailure != len(frame) {
		t.Fatalf("failed commit dropped staged bytes: %d", bytesAfterFailure)
	}
	second, digest2, err := transfer.ClaimCommit("u-retry", handle)
	if err != nil {
		t.Fatalf("retry claim failed: %v", err)
	}
	if !bytes.Equal(first, second) || digest != digest2 {
		t.Fatal("retry did not return identical bytes and digest")
	}
	transfer.FinishCommit("u-retry", handle, CommitSucceeded)
	transfer.mu.Lock()
	left, sessions := transfer.aggregate, len(transfer.sessions)
	transfer.mu.Unlock()
	if left != 0 || sessions != 0 {
		t.Fatalf("successful retry leaked budget: bytes=%d sessions=%d", left, sessions)
	}
}


// --- Ambiguous final-save outcomes ------------------------------------------

// ambiguityTransport drops the SaveUploadedFile reply. With dispatchFirst=true
// the controller processes the mutation but the caller sees only a transport
// error (lost reply / timeout / cancel). With false the request never reaches
// the controller, so destination verification must fail.
type ambiguityTransport struct {
	base          http.RoundTripper
	err           error
	dispatchFirst bool
	once          sync.Once
	dispatched    chan struct{}
}

func (a *ambiguityTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(req.URL.Path, "/carbonpanel.v1.FileService/SaveUploadedFile") {
		return a.base.RoundTrip(req)
	}
	if a.dispatchFirst {
		resp, err := a.base.RoundTrip(req) // controller performs the mutation
		a.once.Do(func() { close(a.dispatched) })
		if err == nil && resp != nil {
			_ = resp.Body.Close()
		}
		return nil, a.err // reply lost after dispatch
	}
	a.once.Do(func() { close(a.dispatched) })
	return nil, a.err // never dispatched
}

// TestIntegrationControllerStreamAmbiguousFinalSave proves a post-dispatch
// unknown outcome is resolved by VERIFYING the destination, never by a blind
// second upload. A lost reply after the save still succeeds with verification;
// a save that never reached the controller returns a truthful UNKNOWN result.
func TestIntegrationControllerStreamAmbiguousFinalSave(t *testing.T) {
	cases := []struct {
		name          string
		err           error
		dispatchFirst bool
		wantVerified  bool
	}{
		{"lost_reply", errors.New("connection reset after dispatch"), true, true},
		{"timeout", context.DeadlineExceeded, true, true},
		{"cancel", context.Canceled, true, true},
		{"never_dispatched", context.DeadlineExceeded, false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newControllerStreamFixture(t)
			acc, err := nodestorage.NewLocal(f.root)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = acc.Close() }()
			payload := bytes.Repeat([]byte("A"), 300<<10)
			if err := acc.WriteFile("worlds/ambiguous.bin", payload); err != nil {
				t.Fatal(err)
			}

			gate := &ambiguityTransport{
				base: f.ts.Client().Transport, err: tc.err,
				dispatchFirst: tc.dispatchFirst, dispatched: make(chan struct{}),
			}
			httpClient := &http.Client{Transport: gate}
			token := seedToken(t, f.store, f.enforcer, "u-ambiguous", "admin", nil)
			clients, err := NewClients(httpClient, f.ts.URL, token)
			if err != nil {
				t.Fatal(err)
			}
			session := connectStdioSession(t, NewServer(clients), context.Background())

			opened := callTool(t, session, "scoped_stream_open", map[string]any{
				"server_id": "srv-1", "path": "worlds/ambiguous.bin", "total_size": len(payload),
			})
			assertSuccess(t, opened, "open")
			handle := resultString(opened, "session_handle")
			for {
				out := callTool(t, session, "scoped_stream_chunk", map[string]any{"session_handle": handle})
				assertSuccess(t, out, "chunk")
				m, _ := out.Result.(map[string]any)
				if done, _ := m["done"].(bool); done {
					break
				}
			}

			invocationRecorder.enable()
			defer invocationRecorder.disable()
			before := len(invocationRecorder.snapshot())
			commit := callTool(t, session, "scoped_stream_commit", map[string]any{
				"session_handle": handle, "destination": "worlds",
				"filename": "ambiguous-copy.bin", "confirm": true,
			})
			select {
			case <-gate.dispatched:
			default:
				t.Fatal("SaveUploadedFile was never dispatched")
			}
			if tc.wantVerified {
				assertSuccess(t, commit, "commit after ambiguous save")
				if resultString(commit, "verification") == "" {
					t.Fatalf("commit did not report destination verification: %+v", commit)
				}
			} else {
				assertToolError(t, commit, "never-dispatched save")
				if commit.Error == nil || !strings.Contains(commit.Error.Message, "UNKNOWN") ||
					!strings.Contains(commit.Error.Message, "do not blindly retry") {
					t.Fatalf("UNKNOWN outcome lacks recovery hint: %+v", commit.Error)
				}
			}

			// Exactly one commit sequence; verification reads the destination
			// but NO second InitUpload/UploadChunk/SaveUploadedFile is dispatched.
			counts := map[string]int{}
			for _, rec := range invocationRecorder.snapshot()[before:] {
				counts[rec.Procedure]++
			}
			wantChunks := (len(payload) + storageworker.ChunkBytes - 1) / storageworker.ChunkBytes
			wantSave := 1
			if !tc.dispatchFirst {
				wantSave = 0 // the request never reached the controller
			}
			if counts["/carbonpanel.v1.UploadService/InitUpload"] != 1 ||
				counts["/carbonpanel.v1.UploadService/UploadChunk"] != wantChunks ||
				counts["/carbonpanel.v1.FileService/SaveUploadedFile"] != wantSave {
				t.Fatalf("ambiguous save caused a second upload sequence: %+v", counts)
			}
			if tc.wantVerified && counts["/carbonpanel.v1.FileService/GetFile"] != 1 {
				t.Fatalf("destination was not verified: %+v", counts)
			}
			// The handle is terminal after either outcome; a fresh upload is
			// impossible because the lease/budget are released exactly once.
			again := callTool(t, session, "scoped_stream_commit", map[string]any{
				"session_handle": handle, "destination": "worlds",
				"filename": "ambiguous-copy.bin", "confirm": true,
			})
			assertToolError(t, again, "retry after ambiguous save")
			t.Logf("ambiguous save %s: verified=%v, one upload sequence, terminal lease", tc.name, tc.wantVerified)
		})
	}
}
