// SPDX-License-Identifier: MPL-2.0
package storageworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"sync"
)

// callContext closes the whole session if cancellation interrupts a frame. A
// partially exchanged JSON line cannot safely be reused by another operation.
func (s *Session) callContext(ctx context.Context, r Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	stop := context.AfterFunc(ctx, func() { _ = s.Close() })
	defer stop()
	r.Version = ProtocolVersion
	res, err := s.callOK(r)
	if ctx.Err() != nil {
		return res, errors.Join(ctx.Err(), err)
	}
	return res, err
}
func (r *RemoteAccessor) OpenRead(ctx context.Context, rel string) (ReadHandle, error) {
	if r.s.version != ProtocolVersion {
		return nil, fmt.Errorf("storageworker: worker protocol %d does not support streaming; update worker image", r.s.version)
	}
	res, err := r.s.callContext(ctx, Request{Op: "stream_open", Path: rel})
	if err != nil {
		return nil, err
	}
	return &remoteReader{s: r.s, ctx: ctx, id: res.Handle, sum: sha256.New()}, nil
}
func (r *RemoteAccessor) BeginWrite(ctx context.Context, rel string, limit int64) (AtomicWriter, error) {
	if r.s.version != ProtocolVersion {
		return nil, fmt.Errorf("storageworker: worker protocol %d does not support streaming; update worker image", r.s.version)
	}
	if limit <= 0 || limit > MaxStreamBytes {
		return nil, fmt.Errorf("storageworker: invalid stream limit %d", limit)
	}
	res, err := r.s.callContext(ctx, Request{Op: "stream_create", Path: rel, Limit: limit})
	if err != nil {
		return nil, err
	}
	return &remoteWriter{s: r.s, ctx: ctx, id: res.Handle, sum: sha256.New()}, nil
}

type remoteReader struct {
	mu          sync.Mutex
	s           *Session
	ctx         context.Context
	id, seq     uint64
	sum         hash.Hash
	buf         []byte
	eof, closed bool
	err         error
}

func (r *remoteReader) ReadAt(p []byte, off int64) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return 0, os.ErrClosed
	}
	if off < 0 {
		return 0, fmt.Errorf("storageworker: negative read offset")
	}
	n := 0
	for len(p) > 0 {
		size := min(len(p), ChunkBytes)
		res, err := r.s.callContext(r.ctx, Request{Op: "stream_read_at", Handle: r.id, Offset: off, Length: size})
		if err != nil {
			return n, err
		}
		if res.Offset != off || len(res.Data) > size || checksum(res.Data) != res.Checksum {
			return n, fmt.Errorf("storageworker: corrupt random-read frame")
		}
		copied := copy(p, res.Data)
		n += copied
		p = p[copied:]
		off += int64(copied)
		if copied < size {
			return n, io.EOF
		}
	}
	return n, nil
}

func (r *remoteReader) Read(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if r.closed {
		return 0, os.ErrClosed
	}
	if len(p) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}
	if len(r.buf) == 0 {
		if r.eof {
			return 0, io.EOF
		}
		res, err := r.s.callContext(r.ctx, Request{Op: "stream_read", Handle: r.id, Sequence: r.seq})
		if err != nil {
			r.err = err
			return 0, err
		}
		if res.Sequence != r.seq || len(res.Data) > ChunkBytes || checksum(res.Data) != res.Checksum {
			r.err = fmt.Errorf("storageworker: corrupt read frame")
			return 0, r.err
		}
		r.seq++
		_, _ = r.sum.Write(res.Data)
		r.eof = res.EOF
		if r.eof && hex.EncodeToString(r.sum.Sum(nil)) != res.Digest {
			r.err = fmt.Errorf("storageworker: stream checksum mismatch")
			return 0, r.err
		}
		r.buf = res.Data
		if len(r.buf) == 0 {
			if r.eof {
				return 0, io.EOF
			}
			r.err = io.ErrNoProgress
			return 0, r.err
		}
	}
	n := copy(p, r.buf)
	r.buf = r.buf[n:]
	return n, nil
}
func (r *remoteReader) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	_, err := r.s.callOK(Request{Version: ProtocolVersion, Op: "stream_close", Handle: r.id})
	return err
}

type remoteWriter struct {
	s                 *Session
	ctx               context.Context
	id, seq           uint64
	sum               hash.Hash
	closed, committed bool
	err               error
}

func (w *remoteWriter) Write(p []byte) (int, error) {
	if w.closed {
		return 0, os.ErrClosed
	}
	if w.err != nil {
		return 0, w.err
	}
	n := 0
	for len(p) > 0 {
		size := min(len(p), ChunkBytes)
		chunk := p[:size]
		res, err := w.s.callContext(w.ctx, Request{Op: "stream_write", Handle: w.id, Sequence: w.seq, Data: chunk, Checksum: checksum(chunk)})
		if err == nil && res.Sequence != w.seq {
			err = fmt.Errorf("storageworker: write acknowledgement mismatch")
		}
		if err != nil {
			w.err = err
			return n, err
		}
		_, _ = w.sum.Write(chunk)
		w.seq++
		n += size
		p = p[size:]
	}
	return n, nil
}
func (w *remoteWriter) Commit() error {
	if w.committed {
		return nil
	}
	if w.closed {
		return os.ErrClosed
	}
	if w.err != nil {
		return w.err
	}
	_, err := w.s.callContext(w.ctx, Request{Op: "stream_commit", Handle: w.id, Sequence: w.seq, Checksum: hex.EncodeToString(w.sum.Sum(nil))})
	if err != nil {
		w.err = err
		return err
	}
	w.closed = true
	w.committed = true
	return nil
}
func (w *remoteWriter) Close() error {
	if w.closed {
		return nil
	}
	w.closed = true
	_, err := w.s.callOK(Request{Version: ProtocolVersion, Op: "stream_close", Handle: w.id})
	return err
}
