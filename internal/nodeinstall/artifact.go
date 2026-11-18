// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package nodeinstall

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// ArtifactManifest is the integrity record for one versioned installer
// artifact. Files maps a relative, slash-separated path to its SHA-256 hex.
type ArtifactManifest struct {
	Schema    string            `json:"schema"`
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	OS        string            `json:"os"`
	Arch      string            `json:"arch"`
	Files     map[string]string `json:"files"`
	Signature string            `json:"signature,omitempty"`
}

// InstallState is the secret-free on-guest state. Two slots hold the active
// and the immediately previous version, which makes rollback a state flip
// rather than a re-download.
type SlotInfo struct {
	Version  string            `json:"version"`
	Manifest *ArtifactManifest `json:"manifest"`
}

type InstallState struct {
	Schema        string               `json:"schema"`
	NodeID        string               `json:"node_id"`
	ActiveSlot    string               `json:"active_slot"`
	ActiveVersion string               `json:"active_version"`
	Slots         map[string]*SlotInfo `json:"slots"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

type upgradeJournal struct {
	TargetSlot string `json:"target_slot"`
	Version    string `json:"version"`
	Phase      string `json:"phase"`
}

const journalFile = "upgrade.journal"

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func canonicalManifestBytes(m *ArtifactManifest) ([]byte, error) {
	copyOf := *m
	copyOf.Signature = ""
	return json.Marshal(&copyOf)
}

func safeRelPath(rel string) error {
	if rel == "" || filepath.IsAbs(rel) || strings.HasPrefix(rel, "..") || strings.Contains(rel, "\\") {
		return fmt.Errorf("%w: unsafe artifact path %q", ErrIntegrity, rel)
	}
	return nil
}

// VerifyArtifact checks a bundle's platform, checksum manifest and optional
// Ed25519 signature before any file is installed. A signature without a
// supplied public key is refused (fail closed). targetOS/targetArch allow
// offline fixtures on a non-Linux host to verify a Linux artifact; production
// callers pass runtime.GOOS/runtime.GOARCH.
func VerifyArtifact(dir, targetOS, targetArch string, pub *ed25519.PublicKey) (*ArtifactManifest, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	var m ArtifactManifest
	if err := readJSON(filepath.Join(abs, manifestName), &m); err != nil {
		return nil, err
	}
	if m.Schema != "carbon-panel-node-artifact/v1" || m.Version == "" || len(m.Files) == 0 {
		return nil, fmt.Errorf("%w: artifact manifest is incomplete", ErrIntegrity)
	}
	if targetOS == "" {
		targetOS = runtime.GOOS
	}
	if targetArch == "" {
		targetArch = runtime.GOARCH
	}
	if m.OS != targetOS || m.Arch != targetArch {
		return nil, fmt.Errorf("%w: artifact is %s/%s, target is %s/%s", ErrPlatform, m.OS, m.Arch, targetOS, targetArch)
	}
	if m.Signature != "" {
		if pub == nil {
			return nil, fmt.Errorf("%w: artifact is signed but no public key was supplied", ErrIntegrity)
		}
		sig, err := base64.StdEncoding.DecodeString(m.Signature)
		if err != nil {
			return nil, fmt.Errorf("%w: artifact signature encoding: %v", ErrIntegrity, err)
		}
		canonical, err := canonicalManifestBytes(&m)
		if err != nil {
			return nil, err
		}
		if !ed25519.Verify(*pub, canonical, sig) {
			return nil, fmt.Errorf("%w: artifact signature does not match", ErrIntegrity)
		}
	}
	for rel, want := range m.Files {
		if err := safeRelPath(rel); err != nil {
			return nil, err
		}
		got, err := hashFile(filepath.Join(abs, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("%w: artifact file %s: %v", ErrIntegrity, rel, err)
		}
		if got != want {
			return nil, fmt.Errorf("%w: artifact file %s hash mismatch", ErrIntegrity, rel)
		}
	}
	// Refuse unexpected files so unsigned content cannot ride along.
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || e.Name() == manifestName {
			continue
		}
		if _, ok := m.Files[e.Name()]; !ok {
			return nil, fmt.Errorf("%w: unexpected file in artifact: %s", ErrIntegrity, e.Name())
		}
	}
	return &m, nil
}

func copyDirTree(src, dst string) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		return err
	}
	for _, e := range entries {
		s := filepath.Join(src, e.Name())
		d := filepath.Join(dst, e.Name())
		if e.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: artifact contains a symlink: %s", ErrIntegrity, e.Name())
		}
		if e.IsDir() {
			if err := copyDirTree(s, d); err != nil {
				return err
			}
			continue
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		in, err := os.Open(s)
		if err != nil {
			return err
		}
		out, err := os.OpenFile(d, os.O_CREATE|os.O_EXCL|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			_ = in.Close()
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		_ = in.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func slotDir(stateDir, slot string) string {
	return filepath.Join(stateDir, "slots", slot)
}

func stageDir(stateDir, slot string) string {
	return filepath.Join(stateDir, "slots", slot+".stage")
}

func loadState(stateDir string) (*InstallState, error) {
	var st InstallState
	if err := readJSON(filepath.Join(stateDir, stateFile), &st); err != nil {
		return nil, err
	}
	if st.Slots == nil {
		return nil, fmt.Errorf("%w: install state has no slots", ErrIntegrity)
	}
	return &st, nil
}

func saveState(stateDir string, st *InstallState) error {
	st.Schema = "carbon-panel-node-install-state/v1"
	st.UpdatedAt = time.Now().UTC()
	return writeJSON(filepath.Join(stateDir, stateFile), st, 0o644)
}

// recoverIfNeeded finishes an interrupted promotion. A journal at phase
// "staged" is completed by renaming the staged slot into place; a journal at
// "promoted" is completed by committing the state file. Anything else blocks
// a retry with a distinct refusal rather than guessing an outcome.
func recoverIfNeeded(stateDir string) error {
	jp := filepath.Join(stateDir, journalFile)
	b, err := os.ReadFile(jp)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var j upgradeJournal
	if err := json.Unmarshal(b, &j); err != nil {
		return fmt.Errorf("%w: unreadable upgrade journal: %v", ErrUnresolvedRecovery, err)
	}
	switch j.Phase {
	case "staged":
		if _, err := os.Stat(slotDir(stateDir, j.TargetSlot)); errors.Is(err, os.ErrNotExist) {
			if _, serr := os.Stat(stageDir(stateDir, j.TargetSlot)); serr == nil {
				if err := os.Rename(stageDir(stateDir, j.TargetSlot), slotDir(stateDir, j.TargetSlot)); err != nil {
					return fmt.Errorf("%w: finishing staged slot: %v", ErrUnresolvedRecovery, err)
				}
			} else {
				return fmt.Errorf("%w: staged slot is missing", ErrUnresolvedRecovery)
			}
		}
	case "promoted":
		if _, err := os.Stat(slotDir(stateDir, j.TargetSlot)); err != nil {
			return fmt.Errorf("%w: promoted slot is missing", ErrUnresolvedRecovery)
		}
		st, err := loadState(stateDir)
		if err != nil {
			return err
		}
		if st.ActiveSlot != j.TargetSlot {
			st.ActiveSlot = j.TargetSlot
			st.ActiveVersion = j.Version
			if err := saveState(stateDir, st); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%w: unknown journal phase %q", ErrUnresolvedRecovery, j.Phase)
	}
	return os.Remove(jp)
}

func slotFilesMatch(dir string, m *ArtifactManifest) error {
	for rel, want := range m.Files {
		if err := safeRelPath(rel); err != nil {
			return err
		}
		got, err := hashFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("%w: slot file %s: %v", ErrIntegrity, rel, err)
		}
		if got != want {
			return fmt.Errorf("%w: slot file %s hash mismatch", ErrIntegrity, rel)
		}
	}
	return nil
}

// Upgrade verifies a new artifact, stages it into the inactive slot and
// promotes it with an atomic state-file update. The persistent data directory
// is never passed in and is never touched. Repeating an upgrade at the same
// version is a no-op.
func Upgrade(stateDir, artifactDir, targetOS, targetArch string, pub *ed25519.PublicKey) (*InstallState, error) {
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return nil, err
	}
	if err := recoverIfNeeded(dir); err != nil {
		return nil, err
	}
	m, err := VerifyArtifact(artifactDir, targetOS, targetArch, pub)
	if err != nil {
		return nil, err
	}
	id, err := InitIdentity(dir)
	if err != nil {
		return nil, err
	}
	var st *InstallState
	if err := readJSON(filepath.Join(dir, stateFile), &st); errors.Is(err, ErrNotFound) {
		st = &InstallState{Slots: map[string]*SlotInfo{}}
	} else if err != nil {
		return nil, err
	} else if st.Slots == nil {
		return nil, fmt.Errorf("%w: install state has no slots", ErrIntegrity)
	}
	if st.ActiveSlot != "" && st.ActiveVersion == m.Version {
		if cur := st.Slots[st.ActiveSlot]; cur != nil && cur.Manifest != nil {
			if err := slotFilesMatch(slotDir(dir, st.ActiveSlot), cur.Manifest); err == nil {
				return st, nil // idempotent
			}
		}
	}
	target := slotA
	if st.ActiveSlot == slotA {
		target = slotB
	}
	stage := stageDir(dir, target)
	if err := os.RemoveAll(stage); err != nil {
		return nil, err
	}
	if err := copyDirTree(artifactDir, stage); err != nil {
		return nil, err
	}
	if err := writeJSON(filepath.Join(stage, manifestName), m, 0o644); err != nil {
		return nil, err
	}
	if _, err := VerifyArtifact(stage, targetOS, targetArch, pub); err != nil {
		return nil, fmt.Errorf("%w: staged artifact: %v", ErrIntegrity, err)
	}

	jp := filepath.Join(dir, journalFile)
	if err := writeJSON(jp, &upgradeJournal{TargetSlot: target, Version: m.Version, Phase: "staged"}, 0o644); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(slotDir(dir, target)); err != nil {
		return nil, err
	}
	if err := os.Rename(stage, slotDir(dir, target)); err != nil {
		return nil, err
	}
	if err := writeJSON(jp, &upgradeJournal{TargetSlot: target, Version: m.Version, Phase: "promoted"}, 0o644); err != nil {
		return nil, err
	}
	st.NodeID = id.NodeID
	st.ActiveSlot = target
	st.ActiveVersion = m.Version
	st.Slots[target] = &SlotInfo{Version: m.Version, Manifest: m}
	if err := saveState(dir, st); err != nil {
		return nil, err
	}
	if err := os.Remove(jp); err != nil {
		return nil, err
	}
	return st, nil
}

// Rollback verifies the inactive slot's undo manifest before flipping back to
// it. A damaged undo is refused without changing the active version.
func Rollback(stateDir string) (*InstallState, error) {
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return nil, err
	}
	if err := recoverIfNeeded(dir); err != nil {
		return nil, err
	}
	st, err := loadState(dir)
	if err != nil {
		return nil, err
	}
	if st.ActiveSlot == "" {
		return nil, fmt.Errorf("%w: no active version to roll back", ErrInvalidInput)
	}
	target := slotA
	if st.ActiveSlot == slotA {
		target = slotB
	}
	undo := st.Slots[target]
	if undo == nil || undo.Manifest == nil {
		return nil, fmt.Errorf("%w: no previous version to restore", ErrNotFound)
	}
	if err := slotFilesMatch(slotDir(dir, target), undo.Manifest); err != nil {
		return nil, fmt.Errorf("%w: undo verification failed: %v", ErrIntegrity, err)
	}
	jp := filepath.Join(dir, journalFile)
	if err := writeJSON(jp, &upgradeJournal{TargetSlot: target, Version: undo.Version, Phase: "promoted"}, 0o644); err != nil {
		return nil, err
	}
	st.ActiveSlot = target
	st.ActiveVersion = undo.Version
	if err := saveState(dir, st); err != nil {
		return nil, err
	}
	if err := os.Remove(jp); err != nil {
		return nil, err
	}
	return st, nil
}

// Status returns the redacted install state (no secrets) after completing any
// interrupted promotion. If recovery cannot be completed it returns the
// distinct unresolved-recovery refusal.
func Status(stateDir string) (*InstallState, error) {
	dir, err := safeStateDir(stateDir)
	if err != nil {
		return nil, err
	}
	if err := recoverIfNeeded(dir); err != nil {
		return nil, err
	}
	st, err := loadState(dir)
	if errors.Is(err, ErrNotFound) {
		return &InstallState{Slots: map[string]*SlotInfo{}}, nil
	}
	return st, err
}

// SortedManifestPaths is a small helper used by fixtures and doctor output.
func SortedManifestPaths(m *ArtifactManifest) []string {
	out := make([]string, 0, len(m.Files))
	for p := range m.Files {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
