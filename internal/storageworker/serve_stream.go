// SPDX-License-Identifier: MPL-2.0
package storageworker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

type streamHandle struct {
	reader io.ReadCloser
	writer AtomicWriter
	sum    hash.Hash
	seq    uint64
}
type streams struct {
	fs      FS
	next    uint64
	handles map[uint64]*streamHandle
}

func newStreams(fs FS) *streams { return &streams{fs: fs, handles: map[uint64]*streamHandle{}} }
func (s *streams) closeHandle(id uint64) error {
	h := s.handles[id]
	if h == nil {
		return fmt.Errorf("storageworker: unknown stream")
	}
	delete(s.handles, id)
	if h.reader != nil {
		return h.reader.Close()
	}
	return h.writer.Close()
}
func (s *streams) close() {
	for id := range s.handles {
		_ = s.closeHandle(id)
	}
}
func checksum(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }
func (s *streams) dispatch(r Request) Response {
	fail := func(err error) Response { return errorResponse(err) }
	if r.Version != ProtocolVersion {
		return fail(fmt.Errorf("storageworker: incompatible stream protocol %d", r.Version))
	}
	if r.Op == "stream_open" || r.Op == "stream_create" {
		if len(s.handles) >= maxHandles {
			return fail(fmt.Errorf("storageworker: too many open streams"))
		}
		fs, ok := s.fs.(StreamingFS)
		if !ok {
			return fail(fmt.Errorf("storageworker: streaming filesystem unavailable"))
		}
		h := &streamHandle{sum: sha256.New()}
		var err error
		if r.Op == "stream_open" {
			h.reader, err = fs.OpenRead(context.Background(), r.Path)
		} else {
			h.writer, err = fs.BeginWrite(context.Background(), r.Path, r.Limit)
		}
		if err != nil {
			return fail(err)
		}
		s.next++
		s.handles[s.next] = h
		return Response{OK: true, Handle: s.next}
	}
	h := s.handles[r.Handle]
	if h == nil {
		return fail(fmt.Errorf("storageworker: unknown stream"))
	}
	switch r.Op {
	case "stream_read_at":
		ra, ok := h.reader.(io.ReaderAt)
		if !ok || r.Offset < 0 || r.Length < 0 || r.Length > ChunkBytes {
			return fail(fmt.Errorf("storageworker: invalid random-read frame"))
		}
		b := make([]byte, r.Length)
		n, err := ra.ReadAt(b, r.Offset)
		if err != nil && err != io.EOF {
			return fail(err)
		}
		b = b[:n]
		return Response{OK: true, Data: b, Offset: r.Offset, Checksum: checksum(b), EOF: err == io.EOF}
	case "stream_close":
		if err := s.closeHandle(r.Handle); err != nil {
			return fail(err)
		}
		return Response{OK: true}
	case "stream_read":
		if h.reader == nil || r.Sequence != h.seq {
			return fail(fmt.Errorf("storageworker: invalid read sequence"))
		}
		b := make([]byte, ChunkBytes)
		n, err := io.ReadFull(h.reader, b)
		b = b[:n]
		eof := err == io.EOF || err == io.ErrUnexpectedEOF
		if err != nil && !eof {
			_ = s.closeHandle(r.Handle)
			return fail(err)
		}
		_, _ = h.sum.Write(b)
		res := Response{OK: true, Data: b, Sequence: h.seq, Checksum: checksum(b), EOF: eof}
		h.seq++
		if eof {
			res.Digest = hex.EncodeToString(h.sum.Sum(nil))
		}
		return res
	case "stream_write":
		if h.writer == nil || r.Sequence != h.seq || len(r.Data) > ChunkBytes || checksum(r.Data) != r.Checksum {
			_ = s.closeHandle(r.Handle)
			return fail(fmt.Errorf("storageworker: invalid write frame"))
		}
		n, err := h.writer.Write(r.Data)
		if err == nil && n != len(r.Data) {
			err = io.ErrShortWrite
		}
		if err != nil {
			_ = s.closeHandle(r.Handle)
			return fail(err)
		}
		_, _ = h.sum.Write(r.Data)
		h.seq++
		return Response{OK: true, Sequence: r.Sequence}
	case "stream_commit":
		if h.writer == nil || r.Sequence != h.seq || r.Checksum != hex.EncodeToString(h.sum.Sum(nil)) {
			_ = s.closeHandle(r.Handle)
			return fail(fmt.Errorf("storageworker: stream checksum mismatch"))
		}
		delete(s.handles, r.Handle)
		err := h.writer.Commit()
		_ = h.writer.Close()
		if err != nil {
			return fail(err)
		}
		return Response{OK: true}
	default:
		return fail(fmt.Errorf("storageworker: unknown stream operation"))
	}
}
