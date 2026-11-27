// SPDX-License-Identifier: MPL-2.0
package services

import (
	"context"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/db"
	"github.com/athNdev/carbon-panel/internal/indexers/modrinth"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
)

// providerLookupPrefix marks a failed provider lookup so the preview can
// distinguish a provider outage from a genuinely unidentified local upload.
// Provider errors may embed response bodies, request URLs and transport
// details, so the client-visible reason is fixed instead of echoing them.
const (
	providerLookupPrefix = "Provider lookup failed: "
	providerLookupReason = providerLookupPrefix + "Try again later."
)

type modUpdateLookup interface {
	GetProjects(context.Context, []string) ([]modrinth.ProjectDetails, error)
	VersionsByFileHash(context.Context, []string) (map[string]modrinth.Version, error)
	CompatibleUpdatesByFileHash(context.Context, []string, string, string) (map[string]modrinth.Version, error)
}

func modUpdateProvider(settings *db.ServerConfig) *modrinth.Client {
	options := modrinth.Options{UserAgent: "CarbonPanel/1.0 (CarbonPanel-admin)"}
	if settings != nil && settings.ModrinthToken != nil {
		options.Token = *settings.ModrinthToken
	}
	if settings != nil && settings.ModrinthUserAgent != nil && *settings.ModrinthUserAgent != "" {
		options.UserAgent = *settings.ModrinthUserAgent
	}
	return modrinth.NewClientWithOptions(nil, options)
}

func installedModHash(ctx context.Context, root *os.Root, name string, limit int64) (string, int64, error) {
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > limit {
		return "", 0, errors.New("file unavailable or exceeds the preview limit")
	}
	file, err := root.Open(name)
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	digest := sha512.New()
	buf := make([]byte, 64*1024)
	reader := io.LimitReader(file, info.Size()+1)
	var read int64
	for {
		if err := ctx.Err(); err != nil {
			return "", read, err
		}
		n, readErr := reader.Read(buf)
		if n > 0 {
			_, _ = digest.Write(buf[:n])
			read += int64(n)
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", read, readErr
		}
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) || read != info.Size() {
		return "", read, errors.New("file changed during preview")
	}
	return hex.EncodeToString(digest.Sum(nil)), read, nil
}

func previewServerModUpdates(ctx context.Context, dataDir, modsDir string, server *db.Server, mods []*v1.Mod, provider modUpdateLookup) error {
	root, err := os.OpenRoot(dataDir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	base, err := filepath.Rel(dataDir, modsDir)
	if err != nil {
		return err
	}
	loader := string(server.ModLoader)
	if isModpackPlatformLoader(server.ModLoader) {
		loader = string(server.ModpackLoader)
	}
	if loader == "" || server.MCVersion == "" || strings.EqualFold(server.MCVersion, "latest") {
		for _, mod := range mods {
			mod.UpdatePreview = &v1.InstalledModUpdate{State: "review_required", Reason: "A known game version and loader are needed to check compatibility."}
		}
		return ctx.Err()
	}
	byHash := make(map[string][]*v1.Mod)
	var hashes []string
	const perFile = 256 << 20
	var remaining int64 = 4 << 30
	for _, mod := range mods {
		if err := ctx.Err(); err != nil {
			return err
		}
		preview := &v1.InstalledModUpdate{State: "unavailable", Reason: "File unavailable or exceeds the preview limit."}
		mod.UpdatePreview = preview
		dir := base
		if !mod.Enabled {
			dir += "_disabled"
		}
		name, err := modInstallPath(dir, mod.FileName)
		if err != nil {
			continue
		}
		hash, count, err := installedModHash(ctx, root, name, min(remaining, perFile))
		remaining -= count
		if err != nil {
			continue
		}
		preview.InstalledSha512 = hash
		preview.Reason = "The provider could not verify a compatible release."
		if _, present := byHash[hash]; !present {
			hashes = append(hashes, hash)
		}
		byHash[hash] = append(byHash[hash], mod)
	}
	for batch := range slices.Chunk(hashes, 100) {
		current, err := provider.VersionsByFileHash(ctx, batch)
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			for _, hash := range batch {
				for _, mod := range byHash[hash] {
					mod.UpdatePreview.State = "unavailable"
					mod.UpdatePreview.Reason = providerLookupReason
				}
			}
			continue
		}
		next, lookupErr := provider.CompatibleUpdatesByFileHash(ctx, batch, loader, server.MCVersion)
		if err := ctx.Err(); err != nil {
			return err
		}
		projectIDs := make([]string, 0, len(batch))
		for _, h := range batch {
			id := current[h].ProjectID
			if id != "" && !slices.Contains(projectIDs, id) {
				projectIDs = append(projectIDs, id)
			}
		}
		projects := make(map[string]modrinth.ProjectDetails)
		if lookupErr == nil && len(projectIDs) > 0 {
			var details []modrinth.ProjectDetails
			details, lookupErr = provider.GetProjects(ctx, projectIDs)
			if ctx.Err() != nil {
				return ctx.Err()
			}
			for _, project := range details {
				projects[project.ID] = project
			}
		}
		for _, hash := range batch {
			for _, mod := range byHash[hash] {
				classifyModUpdate(mod.UpdatePreview, current[hash], next[hash], projects[current[hash].ProjectID], loader, server.MCVersion, lookupErr)
			}
		}
	}
	return ctx.Err()
}

func classifyModUpdate(p *v1.InstalledModUpdate, installed, candidate modrinth.Version, project modrinth.ProjectDetails, loader, game string, lookupErr error) {
	if lookupErr != nil {
		p.State, p.Reason = "unavailable", providerLookupReason
		return
	}
	if installed.ID == "" || installed.ProjectID == "" || !slices.ContainsFunc(installed.Files, func(f modrinth.File) bool { return f.Hashes.SHA512 == p.InstalledSha512 }) {
		p.State, p.Reason = "unknown_source", "Modrinth does not identify these exact file bytes."
		return
	}
	p.Provider, p.ProjectId, p.InstalledVersionId, p.InstalledVersion = "modrinth", installed.ProjectID, installed.ID, installed.VersionNumber
	if project.ID != installed.ProjectID || (project.ServerSide != "required" && project.ServerSide != "optional") {
		p.State, p.Reason = "review_required", "The provider did not confirm server-side support for this project."
		return
	}
	if candidate.ID == "" {
		p.State, p.Reason = "review_required", "No compatible release was reported."
		return
	}
	if candidate.ProjectID != installed.ProjectID || candidate.VersionType != "release" || candidate.Status != "listed" || !slices.Contains(candidate.Loaders, loader) || !slices.Contains(candidate.GameVersions, game) {
		p.State, p.Reason = "review_required", "Provider version does not match the installed project, loader and game version."
		return
	}
	p.CandidateVersionId, p.CandidateVersion = candidate.ID, candidate.VersionNumber
	if candidate.ID == installed.ID {
		p.State, p.Reason = "current", "Already on the compatible release."
		return
	}
	installedAt, err := time.Parse(time.RFC3339, installed.DatePublished)
	candidateAt, dateErr := time.Parse(time.RFC3339, candidate.DatePublished)
	if err != nil || dateErr != nil || !candidateAt.After(installedAt) {
		p.State, p.Reason = "review_required", "The provider did not report a newer release."
		return
	}
	for _, f := range candidate.Files {
		if !f.Primary && len(candidate.Files) != 1 {
			continue
		}
		hash, err := hex.DecodeString(f.Hashes.SHA512)
		if err != nil || len(hash) != sha512.Size || filepath.Base(f.Filename) != f.Filename || strings.ContainsAny(f.Filename, "/\\") || !strings.HasSuffix(strings.ToLower(f.Filename), ".jar") || f.FileType != "" {
			continue
		}
		if p.CandidateSha512 != "" {
			p.CandidateFilename, p.CandidateSha512 = "", ""
			break
		}
		p.CandidateSha512, p.CandidateFilename = f.Hashes.SHA512, f.Filename
	}
	if p.CandidateSha512 == "" {
		p.State, p.Reason = "review_required", "No unambiguous release JAR and checksum were reported."
		return
	}
	for _, dep := range candidate.Dependencies {
		if dep.DependencyType == "required" || dep.DependencyType == "incompatible" {
			p.State, p.Reason = "review_required", "Review the candidate's dependencies before changing the server."
			return
		}
	}
	p.State, p.Reason = "available", fmt.Sprintf("Release %s matches this loader and game version.", candidate.VersionNumber)
}
