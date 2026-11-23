// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package staging

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"strings"

	storage "github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"github.com/athNdev/carbon-panel/internal/storageworker"
)

const (
	modCandidateProviderName     = "modrinth"
	ModCandidateMetadataFileName = ".candidate.json"
)

// ModCandidateMetadata identifies the reviewed provider release represented by
// a staged mod payload. It is stored in a sidecar next to the payload so the
// database schema does not need to change when provider review rules evolve.
type ModCandidateMetadata struct {
	Provider    string `json:"provider"`
	VersionID   string `json:"version_id"`
	ProjectID   string `json:"project_id"`
	SHA512      string `json:"sha512"`
	Filename    string `json:"filename"`
	Loader      string `json:"loader"`
	GameVersion string `json:"game_version"`
}

type modCandidateProvider interface {
	GetVersion(context.Context, string) (*modrinth.Version, error)
	GetProjects(context.Context, []string) ([]modrinth.ProjectDetails, error)
}

func modCandidateMetadataPath(stagePath string) string {
	return path.Join(path.Dir(stagePath), ModCandidateMetadataFileName)
}

// WriteModCandidateMetadata writes the durable review identity beside one
// staged payload. The caller owns the surrounding stage directory and must
// remove it if the following database insert fails.
func WriteModCandidateMetadata(ctx context.Context, acc nodestorage.Accessor, stagePath string, metadata ModCandidateMetadata) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if stagePath == "" {
		return errors.New("staged mod candidate path is required")
	}
	if err := validateModCandidateMetadata(metadata); err != nil {
		return err
	}
	body, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode staged mod candidate metadata: %w", err)
	}
	if err := acc.WriteFile(modCandidateMetadataPath(stagePath), body); err != nil {
		return fmt.Errorf("write staged mod candidate metadata: %w", err)
	}
	return nil
}

// ReadModCandidateMetadata loads and validates the review identity stored
// beside a staged payload. Missing or malformed metadata is a publication
// refusal: an unreviewed candidate must never reach the live tree.
func ReadModCandidateMetadata(acc nodestorage.Accessor, stagePath string) (*ModCandidateMetadata, error) {
	if stagePath == "" {
		return nil, errors.New("staged mod candidate path is required")
	}
	body, err := acc.ReadFile(modCandidateMetadataPath(stagePath))
	if err != nil {
		return nil, fmt.Errorf("read staged mod candidate metadata: %w", err)
	}
	var metadata ModCandidateMetadata
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&metadata); err != nil {
		return nil, fmt.Errorf("decode staged mod candidate metadata: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return nil, fmt.Errorf("decode staged mod candidate metadata: %w", err)
	}
	if err := validateModCandidateMetadata(metadata); err != nil {
		return nil, fmt.Errorf("validate staged mod candidate metadata: %w", err)
	}
	return &metadata, nil
}

// validateStagedModCandidates repeats the provider compatibility decision for
// reviewed mod candidates before any live bytes are changed. Uploaded mods do
// not have candidate metadata and are left to the existing content checks.
func validateStagedModCandidates(ctx context.Context, store *storage.Store, server *storage.Server, acc nodestorage.Accessor, rows []storage.StagedContentChange) error {
	if server == nil || store == nil {
		return errors.New("staged mod candidate validation requires a server and store")
	}
	// The caller may hold a server snapshot from before the batch became due.
	// Always compare the durable candidate against the server's current
	// version and effective loader at publication time.
	current, err := store.GetServer(ctx, server.ID)
	if err != nil {
		return fmt.Errorf("load current server for staged mod candidate validation: %w", err)
	}
	server = current
	var provider modCandidateProvider
	versions := make(map[string]*modrinth.Version)
	projects := make(map[string]modrinth.ProjectDetails)
	for _, row := range rows {
		if row.Kind != storage.StagedKindMod || row.StagePath == "" {
			continue
		}
		// ExpectedLiveSha512 marks a provider-reviewed replacement. Uploaded
		// mods do not carry that precondition and are not provider candidates.
		if row.ExpectedLiveSha512 == "" {
			continue
		}
		metadata, err := ReadModCandidateMetadata(acc, row.StagePath)
		if err != nil {
			return fmt.Errorf("staged mod candidate %s is missing its compatibility metadata: %w", row.FileName, err)
		}
		loader := effectiveModLoader(server)
		if loader == "" || server.MCVersion == "" || strings.EqualFold(server.MCVersion, "latest") {
			return fmt.Errorf("staged mod candidate %s requires a known server loader and game version", row.FileName)
		}
		if metadata.Loader != loader || metadata.GameVersion != server.MCVersion {
			return fmt.Errorf("staged mod candidate %s was prepared for loader %s and game version %s, not %s and %s", row.FileName, metadata.Loader, metadata.GameVersion, loader, server.MCVersion)
		}
		stagedHash, err := fileSHA512(ctx, acc, row.StagePath, storageworker.MaxStreamBytes)
		if err != nil {
			return fmt.Errorf("hash staged mod candidate %s: %w", row.FileName, err)
		}
		if !strings.EqualFold(stagedHash, metadata.SHA512) {
			return fmt.Errorf("staged mod candidate %s no longer matches its reviewed SHA-512", row.FileName)
		}
		if provider == nil {
			provider, err = newModCandidateProvider(ctx, store)
			if err != nil {
				return err
			}
		}
		version, ok := versions[metadata.VersionID]
		if !ok {
			version, err = provider.GetVersion(ctx, metadata.VersionID)
			if err != nil {
				return fmt.Errorf("look up staged mod candidate %s: %w", row.FileName, err)
			}
			versions[metadata.VersionID] = version
		}
		project, ok := projects[metadata.ProjectID]
		if !ok {
			details, err := provider.GetProjects(ctx, []string{metadata.ProjectID})
			if err != nil {
				return fmt.Errorf("look up staged mod candidate project %s: %w", metadata.ProjectID, err)
			}
			for _, detail := range details {
				projects[detail.ID] = detail
			}
			project, ok = projects[metadata.ProjectID]
			if !ok {
				return fmt.Errorf("staged mod candidate %s project %s was not confirmed", row.FileName, metadata.ProjectID)
			}
		}
		if err := validateModCandidateCompatibility(version, *metadata, project, loader, server.MCVersion); err != nil {
			return fmt.Errorf("staged mod candidate %s: %w", row.FileName, err)
		}
	}
	return nil
}

func validateModCandidateMetadata(metadata ModCandidateMetadata) error {
	if metadata.Provider != modCandidateProviderName {
		return fmt.Errorf("unsupported provider %q", metadata.Provider)
	}
	if metadata.VersionID == "" || metadata.ProjectID == "" || metadata.Filename == "" || metadata.Loader == "" || metadata.GameVersion == "" {
		return errors.New("provider release, project, filename, loader, and game version are required")
	}
	hash, err := hex.DecodeString(metadata.SHA512)
	if err != nil || len(hash) != sha512.Size {
		return errors.New("provider release SHA-512 is invalid")
	}
	if path.Base(metadata.Filename) != metadata.Filename || strings.ContainsAny(metadata.Filename, "/\\") || !strings.HasSuffix(strings.ToLower(metadata.Filename), ".jar") {
		return fmt.Errorf("provider release filename %q is not a bare JAR name", metadata.Filename)
	}
	return nil
}

func validateModCandidateCompatibility(version *modrinth.Version, metadata ModCandidateMetadata, project modrinth.ProjectDetails, loader, game string) error {
	if version == nil || version.ID != metadata.VersionID {
		return errors.New("provider did not confirm the reviewed release")
	}
	if version.ProjectID != metadata.ProjectID || version.Status != "listed" || version.VersionType != "release" {
		return errors.New("provider release is no longer the reviewed listed project release")
	}
	if project.ID != metadata.ProjectID || (project.ServerSide != "required" && project.ServerSide != "optional") {
		return errors.New("provider did not confirm server-side support for the reviewed project")
	}
	if !slices.Contains(version.Loaders, loader) || !slices.Contains(version.GameVersions, game) {
		return fmt.Errorf("provider release is not compatible with loader %s and game version %s", loader, game)
	}
	matches := 0
	for _, file := range version.Files {
		if file.Filename != metadata.Filename || !strings.EqualFold(file.Hashes.SHA512, metadata.SHA512) {
			continue
		}
		if path.Base(file.Filename) != file.Filename || strings.ContainsAny(file.Filename, "/\\") || !strings.HasSuffix(strings.ToLower(file.Filename), ".jar") || file.FileType != "" {
			return errors.New("provider release file is not an unambiguous server JAR")
		}
		if !file.Primary && len(version.Files) != 1 {
			continue
		}
		matches++
	}
	if matches != 1 {
		return errors.New("provider did not confirm one reviewed release JAR")
	}
	for _, dependency := range version.Dependencies {
		if dependency.DependencyType == "required" || dependency.DependencyType == "incompatible" {
			return errors.New("provider release dependencies require review before publication")
		}
	}
	return nil
}

func effectiveModLoader(server *storage.Server) string {
	if server == nil {
		return ""
	}
	switch server.ModLoader {
	case storage.ModLoaderAutoCurseForge, storage.ModLoaderCurseForge, storage.ModLoaderFTBA, storage.ModLoaderModrinth:
		return string(server.ModpackLoader)
	default:
		return string(server.ModLoader)
	}
}

func newModCandidateProvider(ctx context.Context, store *storage.Store) (modCandidateProvider, error) {
	options := modrinth.Options{UserAgent: "CarbonPanel/1.0 (CarbonPanel-admin)"}
	if store != nil {
		settings, err := store.GetServerConfig(ctx, storage.GlobalSettingsID)
		if err != nil && !errors.Is(err, storage.ErrServerConfigNotFound) {
			return nil, fmt.Errorf("load provider settings for staged mod candidate: %w", err)
		}
		if settings != nil {
			if settings.ModrinthToken != nil {
				options.Token = *settings.ModrinthToken
			}
			if settings.ModrinthUserAgent != nil && *settings.ModrinthUserAgent != "" {
				options.UserAgent = *settings.ModrinthUserAgent
			}
		}
	}
	return modrinth.NewClientWithOptions(nil, options), nil
}
