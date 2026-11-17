// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

// Package worldlib implements the World Library domain: immutable revisions,
// provenance, compatibility, safe bindings and version-driven configuration.
package worldlib

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// RevisionIDPrefix marks content-addressed revision ids.
const RevisionIDPrefix = "rev_"

// EntryIDPrefix marks logical library identity ids.
const EntryIDPrefix = "wld_"

// RevisionID derives the immutable id from a whole-archive SHA-256.
func RevisionID(sha256Hex string) string {
	return RevisionIDPrefix + strings.ToLower(strings.TrimSpace(sha256Hex))
}

// NewEntryID builds a library identity id from a generated uuid string.
func NewEntryID(uuid string) string {
	return EntryIDPrefix + strings.ToLower(strings.TrimSpace(uuid))
}

// Confidence values for detect/infer/unknown reporting.
const (
	Detected = "detected"
	Inferred = "inferred"
	Unknown  = "unknown"
)

// Setting classifications for the version-driven configuration schema.
const (
	ClassCreationOnly   = "creation_only"
	ClassOfflineOnly    = "offline_only"
	ClassLiveCommand    = "live_command"
	ClassRestartNeeded  = "restart_required"
	ClassUnsafe         = "unsafe"
)

// Support axes. Axis (b) automation is intentionally not claimed.
const (
	AxisArtifact = "api_backed_artifacts"
	AxisWorld    = "prebuilt_world_catalog"
)

// Observation is the shared provenance envelope for non-authoritative fields.
type Observation struct {
	Source        string
	ObservedAt    time.Time
	Applicability string
	Confidence    string
	Note          string
}

// IsKnown reports whether an observation is authoritative enough to gate on.
func (o Observation) IsKnown() bool {
	return o.Confidence == Detected && o.Note == ""
}

// WorldRef is the cross-feature reference type. A bare folder name is never a
// world identity; dimension is a property, not part of the reference.
type WorldRef struct {
	Kind       string // LIBRARY_BINDING | SERVER_LOCAL | STATIC_IMPORT
	BindingID  string
	RevisionID string
	ServerID   string
	LevelName  string
	Dimension  string
}

// Valid reports whether the reference has the fields its kind requires.
func (r WorldRef) Valid() bool {
	switch r.Kind {
	case "LIBRARY_BINDING":
		return r.BindingID != "" || r.RevisionID != ""
	case "SERVER_LOCAL":
		return r.ServerID != "" && r.LevelName != ""
	case "STATIC_IMPORT":
		return r.RevisionID != ""
	default:
		return false
	}
}

// Errors use sentinel values so RPC handlers can map them to typed Connect codes.
var (
	ErrUnsupported        = errors.New("unsupported world operation")
	ErrUnknownVersion     = errors.New("version or capability is unknown")
	ErrArchiveRejected    = errors.New("archive rejected by security policy")
	ErrChecksumMismatch   = errors.New("checksum mismatch")
	ErrRemoteUnavailable  = errors.New("remote node storage unavailable")
	ErrTwoWriters         = errors.New("two writable bindings to one revision are not supported")
	ErrStaleGeneration    = errors.New("world binding owner is stale")
	ErrUncertainOutcome   = errors.New("world operation outcome is uncertain; resolve it before retrying")
	ErrCredentialBoundary = errors.New("credential may only be sent to the authorized API origin")
)

// UnsupportedReason returns a human-readable, non-actionable reason for a
// disabled transform. These must never be presented as best-effort features.
func UnsupportedReason(name string) (string, bool) {
	reasons := map[string]string{
		"edition_conversion":  "Java and Bedrock world formats are not interchangeable.",
		"in_place_downgrade":  "Downgrading a world destroys data; clone and back up first, or restore an older immutable revision.",
		"arbitrary_mod_removal": "Removing mods cannot be done losslessly; use the moderated mod installation paths.",
		"live_nbt_edit":       "Editing level.dat or region files while the server runs corrupts the world.",
		"shared_writable":     "A shared writable world requires a supported shared-state backend and its own decision; clone on bind instead.",
		"world_catalog_auto":  "No documented, license-compatible prebuilt-world discovery route exists; import manually.",
	}
	reason, ok := reasons[name]
	return reason, ok
}

// MustUnsupported panics for an unknown unsupported key used in code, so a
// typo cannot silently become an empty UI reason.
func MustUnsupported(name string) string {
	reason, ok := UnsupportedReason(name)
	if !ok {
		panic(fmt.Sprintf("unknown unsupported reason %q", name))
	}
	return reason
}
