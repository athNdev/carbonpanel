// SPDX-License-Identifier: MPL-2.0
package storageworker

import (
	"context"
	"io"
)

const (
	// ChunkBytes bounds one binary payload before JSON/base64 encoding.
	ChunkBytes = 256 << 10
	// SmallFileBytes is the common convenience-operation limit on every adapter.
	SmallFileBytes int64 = 8 << 20
	// MaxStreamBytes permits a complete world archive, with per-file limits at its caller.
	MaxStreamBytes  int64 = 64 << 30
	ProtocolVersion       = 2
	maxHandles            = 16
	maxFrameBytes         = int((SmallFileBytes+2)/3)*4 + (1 << 20)
)

// AtomicWriter promotes only on Commit. Close aborts an uncommitted write and
// removes only its owned temporary file. A failed Commit preserves its temporary
// and reports that path for operator recovery; Close does not remove it.
type AtomicWriter interface {
	io.WriteCloser
	Commit() error
}

// ReadHandle supports sequential streaming and bounded ZIP random access.
type ReadHandle interface {
	io.ReadCloser
	io.ReaderAt
}

type StreamingFS interface {
	OpenRead(context.Context, string) (ReadHandle, error)
	BeginWrite(context.Context, string, int64) (AtomicWriter, error)
}
