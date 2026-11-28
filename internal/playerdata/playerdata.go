// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package playerdata reads deep, read-only player information from the files
// a Minecraft server writes into its data root: Java stats JSON, advancements
// JSON and the playerdata NBT snapshot (OSS-352).
//
// All I/O goes through the W16 instance-owned node resolver and the W17
// bounded, context-aware read handles. Nothing is ever written and no
// controller-local path is ever substituted for a node that owns its storage
// remotely. Files can change while the server is running, so every field is
// labelled with its source, the time it was observed and a confidence value,
// and the result carries a snapshot note that warns about live inconsistency.
//
// This is a snapshot reader, not a live bridge. A running server keeps state
// in memory and flushes to disk periodically; observing the files therefore
// yields a last-saved view, never a guaranteed-current one. A live view would
// require a dedicated bridge/management adapter (RCON, a plugin or a proxy),
// which this package deliberately does not attempt.
package playerdata

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
	"time"

	db "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
)

// Field is one observed value. Source, ObservedAt and Confidence describe
// where the value came from and how much it can be trusted; ConflictNote
// carries a human-readable caveat when a value is unknown, stale or conflicted.
type Field struct {
	Name, Value, Unit string
	Source            string // "on_disk_json" | "on_disk_nbt" | "identity_service" etc.
	ObservedAt        time.Time
	Confidence        string // "detected" | "stale" | "unknown" | "conflict"
	ConflictNote      string
}

// Result is the read-only view for one (server, uuid, kind) triple.
type Result struct {
	Fields       []Field
	SnapshotNote string // e.g. "last saved at ...; server running, may be inconsistent"
}

// Reader resolves node-owned storage and reads bounded snapshots from it.
type Reader struct {
	store        *db.Store
	resolver     *nodestorage.Resolver
	bases        map[string]string
	baseExplicit map[string]bool
}

// Source values reported on fields.
const (
	SourceOnDiskJSON = "on_disk_json"
	SourceOnDiskNBT  = "on_disk_nbt"
)

// Confidence values reported on fields.
const (
	ConfidenceDetected = "detected"
	ConfidenceStale    = "stale"
	ConfidenceUnknown  = "unknown"
	ConfidenceConflict = "conflict"
)

// Supported kind values for Read.
const (
	KindStats        = "stats"
	KindAdvancements = "advancements"
	KindCharacter    = "character"
)

// Default root-relative bases. Minecraft writes stats and advancements under
// <data-root>/stats/<uuid>.json and <data-root>/advancements/<uuid>.json, and
// the player's NBT snapshot under <data-root>/playerdata/<uuid>.dat. Some
// worlds use a level-name directory instead (e.g. <level>/stats/<uuid>.json);
// call SetBase to point a kind at such an alternate base. Bases are always
// interpreted relative to the server data root.
const (
	DefaultStatsBase        = "stats"
	DefaultAdvancementsBase = "advancements"
	DefaultPlayerDataBase   = "playerdata"
)

// Bounds for each snapshot family, all comfortably under SmallFileBytes.
const (
	statsLimit        int64 = 2 << 20
	advancementsLimit int64 = 2 << 20
	nbtLimit                = storageworker.SmallFileBytes
)

var (
	// ErrUnsupportedKind reports an unknown kind passed to Read.
	ErrUnsupportedKind = errors.New("playerdata: unsupported kind")
	// ErrTooLarge reports a file that exceeds its kind's read bound.
	ErrTooLarge = errors.New("playerdata: file exceeds size limit")
	// ErrNotFound reports a snapshot file that is not present on disk. Callers
	// receive a Result with an "unknown" field rather than an error.
	ErrNotFound = errors.New("playerdata: snapshot file not present")
	// ErrInvalidUUID reports a uuid that cannot be safely used as a filename.
	ErrInvalidUUID = errors.New("playerdata: invalid player uuid")
)

// NewReader constructs a read-only snapshot reader. A nil resolver is valid:
// local and shared-storage nodes still work, while transport-only remote nodes
// fail closed with nodestorage.ErrUnavailable.
func NewReader(store *db.Store, resolver *nodestorage.Resolver) *Reader {
	return &Reader{
		store:    store,
		resolver: resolver,
		bases: map[string]string{
			KindStats:        DefaultStatsBase,
			KindAdvancements: DefaultAdvancementsBase,
			KindCharacter:    DefaultPlayerDataBase,
		},
		baseExplicit: map[string]bool{},
	}
}

// SetBase overrides the root-relative base directory for a kind. The base
// must be a safe relative path (no absolute paths, no ".." traversal). It is
// used verbatim as a prefix: <base>/<uuid>.<ext>. This supports layouts such
// as a level-name directory ("world/stats") used by some worlds.
func (r *Reader) SetBase(kind, rel string) error {
	if _, ok := r.bases[kind]; !ok {
		return fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	clean, err := cleanRel(rel)
	if err != nil {
		return err
	}
	r.bases[kind] = clean
	r.baseExplicit[kind] = true
	return nil
}

// Base returns the configured root-relative base for a kind.
func (r *Reader) Base(kind string) (string, error) {
	base, ok := r.bases[kind]
	if !ok {
		return "", fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	return base, nil
}

func cleanRel(rel string) (string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" || strings.HasPrefix(rel, "/") || strings.Contains(rel, `\`) || filepathVolume(rel) != "" {
		return "", fmt.Errorf("playerdata: base must be a relative slash path, got %q", rel)
	}
	clean := path.Clean(rel)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("playerdata: base escapes the data root: %q", rel)
	}
	return clean, nil
}

// filepathVolume is a small helper so a Windows drive prefix ("C:...") is
// rejected without importing path/filepath into the hot read path.
func filepathVolume(p string) string {
	if len(p) >= 2 && p[1] == ':' && ((p[0] >= 'a' && p[0] <= 'z') || (p[0] >= 'A' && p[0] <= 'Z')) {
		return p[:2]
	}
	return ""
}

// canonicalUUID accepts dashed or undashed 32-hex UUIDs. Requiring hex keeps
// the value from being used for path traversal inside the accessor root.
func canonicalUUID(s string) (string, error) {
	s = strings.TrimSpace(strings.ToLower(strings.ReplaceAll(s, "-", "")))
	if len(s) != 32 {
		return "", fmt.Errorf("%w: %q", ErrInvalidUUID, s)
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return "", fmt.Errorf("%w: %q", ErrInvalidUUID, s)
		}
	}
	return s, nil
}

// candidatePaths lists the locations a snapshot file can live in, most
// specific first.
//
// Minecraft writes player data into the level folder: a Paper server stores
// <data-root>/world/playerdata/<uuid>.dat and the matching stats and
// advancements files. The level folder is configurable, so it is resolved
// from the server's own configuration; the vanilla "world" folder is used when
// that configuration is unknown.
//
// Minecraft writes the UUID with its dashes, so the dashed file name is tried
// first. The undashed form is still accepted because some tooling copies the
// files under that name.
//
// The root-relative base stays the last candidate: a server whose data root
// is the world folder itself, or an explicit SetBase override, keeps working.
func (r *Reader) candidatePaths(ctx context.Context, server *db.Server, base, kind, ext, dashed, undashed string) []string {
	var bases []string
	if r.usesDefaultBase(kind, base) {
		level := r.levelName(ctx, server)
		if level != "" {
			bases = append(bases, path.Join(level, base))
		}
	}
	bases = append(bases, base)

	candidates := make([]string, 0, len(bases)*2)
	for _, candidateBase := range bases {
		candidates = append(candidates, path.Join(candidateBase, dashed+ext))
	}
	for _, candidateBase := range bases {
		candidates = append(candidates, path.Join(candidateBase, undashed+ext))
	}
	return candidates
}

// usesDefaultBase reports whether a kind still uses implicit discovery. An
// explicit SetBase override is used verbatim and never re-prefixed with the
// level folder, even if it matches the default base name.
func (r *Reader) usesDefaultBase(kind, base string) bool {
	if r.baseExplicit[kind] {
		return false
	}
	switch kind {
	case KindStats:
		return base == DefaultStatsBase
	case KindAdvancements:
		return base == DefaultAdvancementsBase
	case KindCharacter:
		return base == DefaultPlayerDataBase
	default:
		return false
	}
}

// levelName resolves the server's save folder. An unknown configuration falls
// back to the vanilla "world" folder; the root-relative locations are still
// tried, so an unreadable configuration never hides a snapshot that is stored
// directly in the data root.
func (r *Reader) levelName(ctx context.Context, server *db.Server) string {
	if r.store != nil && server != nil {
		if cfg, err := r.store.GetServerConfig(ctx, server.ID); err == nil {
			if name := cfg.ActualLevelName(); name != "" {
				return name
			}
		}
	}
	return "world"
}

// Read returns a bounded, read-only snapshot for one player data file.
func (r *Reader) Read(ctx context.Context, server *db.Server, uuid, kind string) (Result, error) {
	if err := ctx.Err(); err != nil {
		return Result{}, err
	}
	if server == nil {
		return Result{}, errors.New("playerdata: nil server")
	}
	id, err := canonicalUUID(uuid)
	if err != nil {
		return Result{}, err
	}
	base, err := r.Base(kind)
	if err != nil {
		return Result{}, err
	}

	var ext string
	var limit int64
	var parse func([]byte, *db.Server) []Field
	switch kind {
	case KindStats:
		ext = ".json"
		limit = statsLimit
		parse = parseStats
	case KindAdvancements:
		ext = ".json"
		limit = advancementsLimit
		parse = parseAdvancements
	case KindCharacter:
		ext = ".dat"
		limit = nbtLimit
		parse = parseCharacter
	default:
		return Result{}, fmt.Errorf("%w: %q", ErrUnsupportedKind, kind)
	}
	dashed := id[0:8] + "-" + id[8:12] + "-" + id[12:16] + "-" + id[16:20] + "-" + id[20:32]
	candidates := r.candidatePaths(ctx, server, base, kind, ext, dashed, id)

	// Reproduce services.storageNodeInfo minimally: legacy aliases designate
	// the controller's own daemon; every other id must resolve to a real node
	// row so remote storage is never guessed from a controller-local path.
	info, err := r.nodeInfo(ctx, server)
	if err != nil {
		return Result{}, err
	}
	acc, err := r.resolver.ForNode(ctx, info, server.DataPath)
	if err != nil {
		return Result{}, fmt.Errorf("playerdata: open storage for node %q: %w", info.Name, err)
	}
	defer func() { _ = acc.Close() }()

	var (
		data []byte
		fi   os.FileInfo
		rel  string
	)
	for _, candidate := range candidates {
		rel = candidate
		data, fi, err = readBounded(ctx, acc, rel, limit)
		if err == nil {
			break
		}
		if !errors.Is(err, ErrNotFound) {
			return Result{}, err
		}
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return notPresentResult(kind, candidates[0], server, time.Time{}), nil
		}
		return Result{}, err
	}

	result := Result{Fields: parse(data, server)}
	result.SnapshotNote = snapshotNote(server, fi.ModTime())
	return result, nil
}

// nodeInfo is the minimal equivalent of services.storageNodeInfo. It is kept
// local to avoid an import cycle (services consumes this package).
func (r *Reader) nodeInfo(ctx context.Context, server *db.Server) (nodestorage.NodeInfo, error) {
	if nodestorage.IsLegacyLocalNodeID(server.NodeID) {
		return nodestorage.LocalNode(), nil
	}
	if r.store == nil {
		return nodestorage.NodeInfo{}, fmt.Errorf("playerdata: store required to resolve node %q", server.NodeID)
	}
	node, err := r.store.GetNode(ctx, server.NodeID)
	if err != nil {
		return nodestorage.NodeInfo{}, fmt.Errorf("playerdata: resolve node %q: %w", server.NodeID, err)
	}
	return node.StorageInfo(), nil
}

// readBounded applies the W17 discipline: check ctx, preflight the size, open
// a context-aware handle, bound the byte count, and re-check ctx afterwards.
func readBounded(ctx context.Context, acc nodestorage.Accessor, rel string, limit int64) ([]byte, os.FileInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	fi, err := acc.Stat(rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil, fmt.Errorf("%w: %s", ErrNotFound, rel)
		}
		return nil, nil, fmt.Errorf("playerdata: stat %s: %w", rel, err)
	}
	if fi.Size() > limit {
		return nil, fi, fmt.Errorf("%w: %s is %d bytes (limit %d)", ErrTooLarge, rel, fi.Size(), limit)
	}
	h, err := acc.OpenRead(ctx, rel)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fi, fmt.Errorf("%w: %s", ErrNotFound, rel)
		}
		return nil, fi, err
	}
	defer func() { _ = h.Close() }()
	if err := ctx.Err(); err != nil {
		return nil, fi, err
	}
	data, err := io.ReadAll(io.LimitReader(h, limit+1))
	if err := ctx.Err(); err != nil {
		return nil, fi, err
	}
	if err != nil {
		return nil, fi, fmt.Errorf("playerdata: read %s: %w", rel, err)
	}
	if int64(len(data)) > limit {
		return nil, fi, fmt.Errorf("%w: %s exceeds %d bytes", ErrTooLarge, rel, limit)
	}
	return data, fi, nil
}

func sourceFor(kind string) string {
	if kind == KindCharacter {
		return SourceOnDiskNBT
	}
	return SourceOnDiskJSON
}

func diskConfidence(server *db.Server) string {
	if serverRunning(server.Status) {
		return ConfidenceStale
	}
	return ConfidenceDetected
}

func serverRunning(status db.ServerStatus) bool {
	switch status {
	case db.StatusStopped, db.StatusPaused, db.StatusDeepSleep:
		return false
	default:
		// running, starting, stopping, error, unhealthy, creating and any
		// unknown status are treated as possibly live (fail-closed).
		return true
	}
}

func snapshotNote(server *db.Server, saved time.Time) string {
	savedStr := "unknown"
	if !saved.IsZero() {
		savedStr = saved.UTC().Format(time.RFC3339)
	}
	if serverRunning(server.Status) {
		return fmt.Sprintf("last saved at %s; server running, may be inconsistent; live view requires bridge/management adapter", savedStr)
	}
	return fmt.Sprintf("last saved at %s; safe snapshot (server is not running)", savedStr)
}

func notPresentResult(kind, rel string, server *db.Server, saved time.Time) Result {
	return Result{
		Fields: []Field{{
			Name:         "availability",
			Value:        "not_present",
			Source:       sourceFor(kind),
			ObservedAt:   time.Now().UTC(),
			Confidence:   ConfidenceUnknown,
			ConflictNote: "snapshot file " + rel + " is not present on disk",
		}},
		SnapshotNote: snapshotNote(server, saved),
	}
}

func unknownField(name, source, note string, server *db.Server) Field {
	return Field{
		Name:         name,
		Value:        "unknown",
		Source:       source,
		ObservedAt:   time.Now().UTC(),
		Confidence:   ConfidenceUnknown,
		ConflictNote: note,
	}
}
