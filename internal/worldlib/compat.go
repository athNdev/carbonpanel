// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package worldlib

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/athNdev/carbon-panel/internal/minecraft"
)

// This file implements the read-only compatibility detect/infer/unknown
// report and its dry run. It never writes an archive, a world directory, a
// database row or a server file. Facts that are not observed are labelled
// inferred or unknown with a reason; unsupported transforms are disabled with
// the shared UnsupportedReason keys.

// Report verdicts and transform statuses (kept as plain strings so the RPC
// layer can map them to the generated proto without this package importing it).
const (
	VerdictCompatible       = "compatible"
	VerdictRequiresDecision = "requires_decision"
	VerdictBlocked          = "blocked"
	VerdictUnknown          = "unknown"

	StatusSupported        = "supported"
	StatusRequiresDecision = "requires_decision"
	StatusBlocked          = "blocked"
	StatusUnsupported      = "unsupported"
)

// Target is the server side of a compatibility check. Every field is
// operator-supplied or panel metadata; no target value is read from world
// bytes, so target facts are always reported as inferred or panel-derived.
type Target struct {
	ServerID    string
	MCVersion   string
	Edition     string
	ModLoader   string
	DataVersion string
	OnlineMode  bool
	NodeID      string
}

// Finding is one classified compatibility observation.
type Finding struct {
	Subject     string
	Confidence  string
	Value       string
	Reason      string
	Observation Observation
}

// Transform is one proposed, never-applied change.
type Transform struct {
	Name          string
	Status        string
	Reversibility string
	Downtime      string
	Reason        string
}

// Report is the plain-Go compatibility report. The RPC layer maps these
// exported fields onto CompatibilityFinding/TransformPlan/CompatibilityReport.
// RevisionID is intentionally left for the RPC layer to fill from the
// immutable revision being checked; this engine only sees an archive path.
type Report struct {
	RevisionID      string
	ServerID        string
	TargetMCVersion string
	Overall         string
	Findings        []Finding
	Transforms      []Transform
	Unsupported     map[string]string
}

// Finding returns a finding by subject, for mapping and tests.
func (r *Report) Finding(subject string) (Finding, bool) {
	if r == nil {
		return Finding{}, false
	}
	for _, f := range r.Findings {
		if f.Subject == subject {
			return f, true
		}
	}
	return Finding{}, false
}

// Transform returns a transform by name.
func (r *Report) Transform(name string) (Transform, bool) {
	if r == nil {
		return Transform{}, false
	}
	for _, t := range r.Transforms {
		if t.Name == name {
			return t, true
		}
	}
	return Transform{}, false
}

// Engine produces compatibility reports. It is stateless and safe to reuse.
type Engine struct{}

func NewEngine() *Engine { return &Engine{} }

// protocolVersion is a seam so tests never need network access. It is the
// only cross-check against the version catalog; all data versions used for
// the verdict come from the local table below or from level.dat.
var protocolVersion = minecraft.GetProtocolVersion

// dataVersionByMC documents representative Java world_version values from
// Mojang's release JAR version.json. This is a small, explicit table:
// unknown versions stay unknown rather than being interpolated or guessed.
var dataVersionByMC = map[string]int{
	"1.16.5": 2586,
	"1.17.1": 2730,
	"1.18.2": 3120,
	"1.19.4": 3578,
	"1.20.1": 3635,
	"1.20.4": 3690,
	"1.21":   3953,
	"1.21.1": 3955,
}

func dataVersionForMC(mcVersion string) (int, bool) {
	v := strings.TrimSpace(mcVersion)
	v = strings.TrimPrefix(v, "v")
	if v == "" {
		return 0, false
	}
	n, ok := dataVersionByMC[v]
	return n, ok
}

// targetDataVersion resolves the target's data version from an explicit
// override, then the local catalog. The protocol catalog is a fail-closed
// cross-check only when the local catalog has no entry: an unrecognized
// version never silently becomes "compatible".
func targetDataVersion(target Target) (int, bool, string) {
	if raw := strings.TrimSpace(target.DataVersion); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			return 0, false, "target data version " + raw + " is not a positive integer"
		}
		return n, true, ""
	}
	if n, ok := dataVersionForMC(target.MCVersion); ok {
		return n, true, ""
	}
	if protocolVersion(target.MCVersion) <= 0 {
		return 0, false, "target Minecraft version '" + target.MCVersion + "' is not in the version catalog; compatibility cannot be decided"
	}
	return 0, false, "no documented data version for target Minecraft version '" + target.MCVersion + "'; gate the operation on an operator decision"
}

// unsupportedKeys is the complete set of disabled capabilities. Every key is
// guaranteed to have a shared reason via MustUnsupported.
var unsupportedKeys = []string{
	"arbitrary_mod_removal",
	"edition_conversion",
	"in_place_downgrade",
	"live_nbt_edit",
	"shared_writable",
	"world_catalog_auto",
}

func unsupportedAll() map[string]string {
	out := make(map[string]string, len(unsupportedKeys))
	for _, key := range unsupportedKeys {
		out[key] = MustUnsupported(key)
	}
	return out
}

func newFinding(subject, confidence, value, reason, source, applicability string, now time.Time) Finding {
	o := Observation{
		Source:        source,
		ObservedAt:    now,
		Applicability: applicability,
		Confidence:    confidence,
	}
	// Detected facts carry no caveat: IsKnown() must be true for them.
	if confidence != Detected {
		o.Note = reason
	}
	return Finding{
		Subject:     subject,
		Confidence:  confidence,
		Value:       value,
		Reason:      reason,
		Observation: o,
	}
}

// --- bounded world archive/directory reader ---------------------------------

const (
	maxWorldEntries   = 100_000
	maxWorldFileBytes = 8 << 20
)

type worldEntry struct {
	name  string // slash-separated, relative to the detected archive root
	isDir bool
	size  int64
	zf    *zip.File
}

type worldFS struct {
	dir     string // set for a plain world directory
	zr      *zip.ReadCloser
	root    string // detected single top-level folder of a world ZIP
	entries []worldEntry
}

func (w *worldFS) rel(e worldEntry) string {
	if w.root == "" {
		return e.name
	}
	return strings.TrimPrefix(e.name, w.root)
}

func (w *worldFS) names() []string {
	out := make([]string, 0, len(w.entries))
	for _, e := range w.entries {
		out = append(out, w.rel(e))
	}
	return out
}

func (w *worldFS) has(name string) bool {
	for _, e := range w.entries {
		if !e.isDir && w.rel(e) == name {
			return true
		}
	}
	return false
}

func (w *worldFS) hasDir(name string) bool {
	prefix := strings.TrimSuffix(name, "/") + "/"
	for _, e := range w.entries {
		rel := w.rel(e)
		if rel == name || strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

func (w *worldFS) hasAnyBase(name string) bool {
	for _, n := range w.names() {
		if strings.EqualFold(path.Base(n), name) {
			return true
		}
	}
	return false
}

// topDirs returns the distinct first path segments of the archive.
func (w *worldFS) topDirs() []string {
	set := map[string]bool{}
	for _, n := range w.names() {
		if i := strings.IndexByte(n, '/'); i > 0 {
			set[n[:i]] = true
		}
	}
	out := make([]string, 0, len(set))
	for d := range set {
		out = append(out, d)
	}
	sort.Strings(out)
	return out
}

// regionFiles returns slash paths of every *.mca file under a region folder.
func (w *worldFS) regionFiles() []string {
	var out []string
	for _, n := range w.names() {
		low := strings.ToLower(n)
		if !strings.HasSuffix(low, ".mca") {
			continue
		}
		if low == "region/"+path.Base(low) || strings.Contains(low, "/region/") {
			out = append(out, n)
		}
	}
	sort.Strings(out)
	return out
}

// readFile reads one bounded file by its archive-relative slash path.
func (w *worldFS) readFile(name string) ([]byte, error) {
	for _, e := range w.entries {
		if e.isDir || w.rel(e) != name {
			continue
		}
		if e.size > maxWorldFileBytes {
			return nil, fmt.Errorf("worldlib: %s exceeds %d bytes", name, maxWorldFileBytes)
		}
		if w.dir != "" {
			return os.ReadFile(filepath.Join(w.dir, filepath.FromSlash(e.name)))
		}
		if e.zf == nil {
			return nil, fmt.Errorf("worldlib: %s has no readable source", name)
		}
		if e.zf.UncompressedSize64 > maxWorldFileBytes {
			return nil, fmt.Errorf("worldlib: %s exceeds %d bytes", name, maxWorldFileBytes)
		}
		rc, err := e.zf.Open()
		if err != nil {
			return nil, fmt.Errorf("worldlib: open %s: %w", name, err)
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(io.LimitReader(rc, maxWorldFileBytes+1))
		if err != nil {
			return nil, fmt.Errorf("worldlib: read %s: %w", name, err)
		}
		if len(b) > maxWorldFileBytes {
			return nil, fmt.Errorf("worldlib: %s exceeds %d bytes", name, maxWorldFileBytes)
		}
		return b, nil
	}
	return nil, fmt.Errorf("worldlib: %s not found in archive", name)
}

func (w *worldFS) Close() error {
	if w.zr != nil {
		return w.zr.Close()
	}
	return nil
}

// detectRoot recognises a single wrapper folder around an otherwise complete
// Java world. Multiple top-level folders or a root level.dat mean no prefix.
func detectRoot(entries []worldEntry) string {
	top := map[string]bool{}
	wrappedLevel := map[string]bool{}
	rootMarkers := false
	for _, e := range entries {
		parts := strings.Split(e.name, "/")
		if len(parts) == 1 {
			switch strings.ToLower(parts[0]) {
			case "level.dat", "levelname.txt", "level_values":
				rootMarkers = true
			}
			continue
		}
		top[parts[0]] = true
		if len(parts) == 2 {
			switch strings.ToLower(parts[1]) {
			case "level.dat", "levelname.txt":
				wrappedLevel[parts[0]] = true
			}
		}
	}
	if rootMarkers || len(top) != 1 {
		return ""
	}
	for segment := range top {
		// Only a Java-world wrapper is stripped. A Bedrock .mcworld with a
		// single db/ folder must keep its LevelDB path visible.
		if wrappedLevel[segment] {
			return segment + "/"
		}
	}
	return ""
}

func openWorldFS(ctx context.Context, archivePath string) (*worldFS, error) {
	info, err := os.Stat(archivePath)
	if err != nil {
		return nil, fmt.Errorf("worldlib: stat archive: %w", err)
	}
	w := &worldFS{}
	if info.IsDir() {
		w.dir = archivePath
		err = filepath.WalkDir(archivePath, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if d.Type()&os.ModeSymlink != 0 {
				return nil // never follow links out of the world
			}
			rel, err := filepath.Rel(archivePath, p)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(rel)
			if name == "." {
				return nil
			}
			if len(w.entries) >= maxWorldEntries {
				return fmt.Errorf("worldlib: archive has more than %d entries", maxWorldEntries)
			}
			fi, err := d.Info()
			if err != nil {
				return err
			}
			w.entries = append(w.entries, worldEntry{name: name, isDir: d.IsDir(), size: fi.Size()})
			return nil
		})
		if err != nil {
			_ = w.Close()
			return nil, err
		}
		return w, nil
	}

	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return nil, fmt.Errorf("worldlib: open archive: %w", err)
	}
	w.zr = zr
	for _, f := range zr.File {
		if err := ctx.Err(); err != nil {
			_ = w.Close()
			return nil, err
		}
		if len(w.entries) >= maxWorldEntries {
			_ = w.Close()
			return nil, fmt.Errorf("worldlib: archive has more than %d entries", maxWorldEntries)
		}
		if unsafeMemberName(strings.TrimPrefix(f.Name, "./")) {
			continue
		}
		name, err := canonicalWorldZipMember(f.Name)
		if err != nil {
			continue
		}
		w.entries = append(w.entries, worldEntry{
			name:  name,
			isDir: f.FileInfo().IsDir(),
			size:  int64(f.UncompressedSize64),
			zf:    f,
		})
	}
	w.root = detectRoot(w.entries)
	return w, nil
}

// --- inspection helpers ------------------------------------------------------

type editionResult struct {
	finding         Finding
	extra           []Finding
	facts           map[string]any
	detectedEdition string
	analysis        analysis
}

func inspectEdition(w *worldFS, target Target, archivePath string, now time.Time) editionResult {
	names := w.names()

	// Bedrock: a LevelDB db/ directory plus a level_values file or one of the
	// world_*packs.json manifests.
	bedrock := w.hasDir("db") && (w.hasAnyBase("level_values") || func() bool {
		for _, n := range names {
			base := strings.ToLower(path.Base(n))
			if strings.HasPrefix(base, "world_") && strings.HasSuffix(base, "packs.json") {
				return true
			}
		}
		return false
	}())

	// A Marketplace lock needs an explicit marker. The .mcworld extension by
	// itself is not evidence: ordinary Bedrock exports use it too.
	marketplace := false
	for _, n := range names {
		low := strings.ToLower(n)
		base := path.Base(low)
		if strings.Contains(low, "marketplace") || base == "world_locked.json" ||
			base == "locked.json" || strings.HasPrefix(base, "encrypted_world") {
			marketplace = true
		}
	}
	if !marketplace && w.has("meta.json") {
		if raw, err := w.readFile("meta.json"); err == nil {
			low := strings.ToLower(string(raw))
			if strings.Contains(low, "marketplace") || (strings.Contains(low, `"locked"`) && strings.Contains(low, "true")) {
				marketplace = true
			}
		}
	}
	// A Marketplace lock is only meaningful for a Bedrock-shaped world.
	marketplace = marketplace && (bedrock || strings.Contains(strings.ToLower(archivePath), ".mcworld"))

	targetEdition := strings.ToLower(strings.TrimSpace(target.Edition))
	crossEdition := func(source string) bool {
		return targetEdition != "" && targetEdition != source
	}

	var facts map[string]any
	if w.has("level.dat") {
		if raw, err := w.readFile("level.dat"); err == nil {
			if parsed, parseErr := LevelDataFacts(raw); parseErr == nil && len(parsed) > 0 {
				facts = parsed
			}
		}
	}

	if marketplace {
		return editionResult{
			finding: newFinding(EditionSubject, Unknown, "",
				"Marketplace-locked Bedrock worlds cannot be exported or converted; the world format is not interchangeable.",
				"on_disk_nbt", "bedrock", now),
			detectedEdition: "bedrock",
			analysis:        analysis{marketplace: true},
		}
	}
	if bedrock {
		res := editionResult{
			finding:         newFinding(EditionSubject, Detected, "bedrock", "", "on_disk_nbt", "bedrock", now),
			detectedEdition: "bedrock",
		}
		if crossEdition("bedrock") {
			res.analysis.crossEdition = true
		}
		return res
	}
	if facts != nil {
		confidence := Detected
		reason := ""
		if len(w.regionFiles()) == 0 {
			confidence = Inferred
			reason = "level.dat parsed as Java NBT, but no region/*.mca files were observed."
		}
		res := editionResult{
			finding:         newFinding(EditionSubject, confidence, "java", reason, "on_disk_nbt", "java", now),
			facts:           facts,
			detectedEdition: "java",
		}
		if crossEdition("java") {
			res.analysis.crossEdition = true
		}
		if targetEdition == "" {
			res.extra = append(res.extra, newFinding("target_edition", Inferred, "java",
				"Target edition was not supplied; compatibility is stated relative to the source edition.",
				"panel_db", target.MCVersion, now))
		}
		return res
	}
	return editionResult{
		finding: newFinding(EditionSubject, Unknown, "",
			"No readable Java level.dat NBT and no Bedrock db/ markers were found; the edition cannot be determined.",
			"on_disk_nbt", target.MCVersion, now),
	}
}

// EditionSubject is the finding subject used for the world's edition.
const EditionSubject = "edition"

func inspectDataVersion(facts map[string]any, edition string, target Target, now time.Time) ([]Finding, analysis) {
	var out []Finding
	var a analysis
	if edition == "bedrock" {
		return []Finding{newFinding("data_version", Inferred, "not_applicable",
			"Bedrock worlds do not carry a Java DataVersion; version compatibility must be decided separately.",
			"on_disk_nbt", "bedrock", now)}, a
	}

	applicability := ""
	if v, ok := FactNested(facts, "Version", "Id"); ok {
		applicability = fmt.Sprint(v)
	}
	sourceDV, sourceKnown := FactInt(facts, "DataVersion")
	if !sourceKnown || sourceDV <= 0 {
		out = append(out, newFinding("data_version", Unknown, "",
			"level.dat has no readable DataVersion; no upgrade or downgrade comparison is possible.",
			"on_disk_nbt", applicability, now))
	} else {
		out = append(out, newFinding("data_version", Detected, strconv.FormatInt(sourceDV, 10), "",
			"on_disk_nbt", applicability, now))
	}

	targetDV, targetKnown, note := targetDataVersion(target)
	if !targetKnown {
		out = append(out, newFinding("target_data_version", Unknown, "", note, "panel_db", target.MCVersion, now))
		a.versionDecision = true
		return out, a
	}
	out = append(out, newFinding("target_data_version", Detected, strconv.Itoa(targetDV), "",
		"panel_db", target.MCVersion, now))

	if sourceKnown && sourceDV > 0 {
		if int64(targetDV) >= sourceDV {
			a.cloneBackup = true
		} else {
			a.downgrade = true
			a.cloneBackup = true // the safe alternative, never an in-place edit
		}
	}
	return out, a
}

func inspectLoader(w *worldFS, target Target, applicability string, now time.Time) (Finding, bool) {
	markers := map[string]bool{}
	for _, n := range w.names() {
		base := strings.ToLower(path.Base(n))
		var candidate string
		switch base {
		case "quilt.mod.json":
			candidate = "quilt"
		case "fabric.mod.json":
			candidate = "fabric"
		case "neoforge.mods.toml", "neoforge.mods.json":
			candidate = "neoforge"
		case "mods.toml", "mcmod.info":
			candidate = "forge"
		default:
			continue
		}
		if loader, ok := minecraft.DetectModpackLoader(candidate); ok {
			candidate = string(loader)
		}
		markers[candidate] = true
	}

	var finding Finding
	switch len(markers) {
	case 1:
		var detected string
		for loader := range markers {
			detected = loader
		}
		finding = newFinding("loader", Detected, detected, "", "on_disk_nbt", applicability, now)
	case 0:
		hasMods := w.hasDir("mods")
		for _, n := range w.names() {
			if strings.HasPrefix(strings.ToLower(n), "mods/") && strings.HasSuffix(strings.ToLower(n), ".jar") {
				hasMods = true
			}
		}
		switch {
		case hasMods:
			finding = newFinding("loader", Unknown, "",
				"Mod files are present but no recognizable Fabric, Quilt or Forge marker was found; the loader cannot be determined.",
				"on_disk_nbt", applicability, now)
		case strings.TrimSpace(target.ModLoader) != "":
			finding = newFinding("loader", Inferred, strings.TrimSpace(target.ModLoader),
				"No mod loader markers were found in the archive; the target server's declared loader is unverified.",
				"panel_db", applicability, now)
		default:
			finding = newFinding("loader", Unknown, "",
				"No mod loader markers were found and the target did not declare a loader.",
				"on_disk_nbt", applicability, now)
		}
	default:
		names := make([]string, 0, len(markers))
		for loader := range markers {
			names = append(names, loader)
		}
		sort.Strings(names)
		finding = newFinding("loader", Unknown, "",
			"Conflicting mod loader markers found: "+strings.Join(names, ", ")+"; loaders are never auto-swapped or removed.",
			"on_disk_nbt", applicability, now)
	}

	mismatch := false
	if finding.Confidence == Detected {
		declared := strings.ToLower(strings.TrimSpace(target.ModLoader))
		if declared != "" && declared != strings.ToLower(finding.Value) {
			mismatch = true
		}
	}
	return finding, mismatch
}

func inspectDimensions(w *worldFS, now time.Time) []Finding {
	regions := w.regionFiles()
	overworld := false
	dims := []string{}
	for _, n := range regions {
		if strings.HasPrefix(n, "region/") {
			overworld = true
		}
	}
	for _, d := range w.topDirs() {
		if !strings.HasPrefix(strings.ToLower(d), "world_") {
			continue
		}
		for _, n := range regions {
			if strings.HasPrefix(n, d+"/region/") {
				dims = append(dims, d)
				break
			}
		}
	}
	sort.Strings(dims)
	if overworld {
		dims = append([]string{minecraft.DefaultLevelName}, dims...)
	}

	var dimFinding Finding
	if len(dims) > 0 {
		dimFinding = newFinding("dimensions", Detected, strings.Join(dims, ","), "", "on_disk_nbt", "java", now)
	} else {
		dimFinding = newFinding("dimensions", Unknown, "",
			"No region/*.mca files were found; the dimension layout cannot be determined.",
			"on_disk_nbt", "java", now)
	}

	registriesObserved := w.hasDir("data")
	for _, n := range w.names() {
		low := strings.ToLower(n)
		if strings.Contains(low, "/registry/") || strings.HasPrefix(low, "registry/") {
			registriesObserved = true
		}
	}
	var registryFinding Finding
	if registriesObserved {
		registryFinding = newFinding("registries", Detected, "observed", "", "on_disk_nbt", "java", now)
	} else {
		registryFinding = newFinding("registries", Inferred, "not_observed",
			"Datapack registry data was not observed; missing registries are never silently repaired.",
			"on_disk_nbt", "java", now)
	}
	return []Finding{dimFinding, registryFinding}
}

// packFormatFloat reads the historical pack_format value shapes: a number,
// a numeric string, or an object with min_inclusive/max_inclusive.
func packFormatFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, n > 0
	case string:
		f, err := strconv.ParseFloat(n, 64)
		return f, err == nil && f > 0
	case map[string]any:
		if min, ok := packFormatFloat(n["min_inclusive"]); ok {
			return min, true
		}
		if max, ok := packFormatFloat(n["max_inclusive"]); ok {
			return max, true
		}
	}
	return 0, false
}

// inlinePackFormat reads pack.mcmeta bytes that are already in memory (for
// datapacks nested inside a world ZIP). It never extracts the nested archive.
func inlinePackFormat(raw []byte) (minecraft.PackFormat, bool) {
	var meta struct {
		Pack struct {
			PackFormat       any `json:"pack_format"`
			SupportedFormats any `json:"supported_formats"`
			MinFormat        any `json:"min_format"`
			MaxFormat        any `json:"max_format"`
		} `json:"pack"`
	}
	if err := json.Unmarshal(raw, &meta); err != nil {
		return minecraft.PackFormat{}, false
	}
	min, okMin := packFormatFloat(meta.Pack.MinFormat)
	max, okMax := packFormatFloat(meta.Pack.MaxFormat)
	if !okMin && !okMax {
		min, okMin = packFormatFloat(meta.Pack.SupportedFormats)
		max, okMax = min, okMin
	}
	if !okMin && !okMax {
		min, okMin = packFormatFloat(meta.Pack.PackFormat)
		max, okMax = min, okMin
	}
	if !okMin && !okMax {
		return minecraft.PackFormat{}, false
	}
	if !okMin {
		min = max
	}
	if !okMax {
		max = min
	}
	if max < min {
		min, max = max, min
	}
	return minecraft.PackFormat{Min: min, Max: max}, true
}

func inspectDatapacks(w *worldFS, target Target, now time.Time) (Finding, bool) {
	sawArea := w.hasDir("datapacks")
	var packs, warnings []string
	for _, n := range w.names() {
		low := strings.ToLower(n)
		if !strings.Contains(low, "/datapacks/") && !strings.HasPrefix(low, "datapacks/") {
			continue
		}
		base := path.Base(low)
		name := strings.TrimSuffix(path.Base(n), path.Ext(n))
		var format minecraft.PackFormat
		var ok bool
		switch {
		case strings.HasSuffix(base, ".zip") && w.dir != "":
			// A real on-disk datapack archive: use the shared parser.
			pf, err := minecraft.ReadPackFormat(filepath.Join(w.dir, filepath.FromSlash(n)))
			if err == nil {
				format, ok = pf, pf.Known()
			}
		case strings.HasSuffix(base, ".zip"):
			// Nested inside a world ZIP: read pack.mcmeta without extracting.
			var raw []byte
			raw, err := readNestedPackMCMeta(w, n)
			if err == nil {
				format, ok = inlinePackFormat(raw)
			}
		case base == "pack.mcmeta":
			raw, err := w.readFile(n)
			if err == nil {
				format, ok = inlinePackFormat(raw)
			}
		default:
			continue
		}
		if !ok || !format.Known() {
			packs = append(packs, name+"=unknown_format")
			continue
		}
		packs = append(packs, name+"="+format.String())
		if msg := minecraft.PackFormatWarning(format, target.MCVersion); msg != "" {
			warnings = append(warnings, name+": "+msg)
		}
	}
	sort.Strings(packs)
	sort.Strings(warnings)

	var finding Finding
	switch {
	case len(packs) == 0 && sawArea:
		finding = newFinding("datapacks", Detected, "none", "", "on_disk_nbt", target.MCVersion, now)
	case len(packs) == 0:
		finding = newFinding("datapacks", Inferred, "not_observed",
			"No datapack area was found; datapacks may be supplied by the target server and are never auto-repaired.",
			"on_disk_nbt", target.MCVersion, now)
	default:
		allKnown := true
		for _, p := range packs {
			if strings.HasSuffix(p, "unknown_format") {
				allKnown = false
			}
		}
		confidence, reason := Detected, ""
		if !allKnown {
			confidence, reason = Inferred, "Some datapacks declared no readable pack format; no format is guessed."
		}
		if len(warnings) > 0 {
			reason += " " + strings.Join(warnings, "; ")
		}
		finding = newFinding("datapacks", confidence, strings.Join(packs, ","), reason, "on_disk_nbt", target.MCVersion, now)
	}
	return finding, len(warnings) > 0
}

// readNestedPackMCMeta reads pack.mcmeta from a datapack zip without ever
// writing the nested archive to disk.
func readNestedPackMCMeta(w *worldFS, name string) ([]byte, error) {
	raw, err := w.readFile(name)
	if err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(strings.NewReader(string(raw)), int64(len(raw)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || path.Base(f.Name) != "pack.mcmeta" {
			continue
		}
		if f.UncompressedSize64 > maxWorldFileBytes {
			return nil, fmt.Errorf("worldlib: nested pack.mcmeta exceeds %d bytes", maxWorldFileBytes)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, err
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(io.LimitReader(rc, maxWorldFileBytes+1))
		if err != nil {
			return nil, err
		}
		if len(b) > maxWorldFileBytes {
			return nil, fmt.Errorf("worldlib: nested pack.mcmeta exceeds %d bytes", maxWorldFileBytes)
		}
		return b, nil
	}
	return nil, fmt.Errorf("worldlib: nested datapack %s has no pack.mcmeta", name)
}

func inspectServerMetadata(target Target, now time.Time) ([]Finding, bool) {
	identityValue := "online"
	identityReason := "Player identities are server metadata; no identity is rewritten by a world operation."
	identityMismatch := false
	if !target.OnlineMode {
		identityValue = "offline"
		identityReason += " Offline-mode UUIDs differ from online-mode UUIDs; players will appear as new identities, and identities are never rewritten."
		identityMismatch = true
	}
	if strings.EqualFold(strings.TrimSpace(target.Edition), "bedrock") {
		identityValue = "xuid"
		identityReason += " Bedrock identifies players by XUID, which cannot be converted to Java UUIDs."
	}
	return []Finding{
		newFinding("plugins", Inferred, "server-managed",
			"Plugins are server metadata, not world bytes; no plugin jar is installed or removed.",
			"panel_db", target.MCVersion, now),
		newFinding("resource_packs", Inferred, "server-managed",
			"Resource packs are server metadata; no pack is rewritten.",
			"panel_db", target.MCVersion, now),
		newFinding("player_identity", Inferred, identityValue, identityReason,
			"identity_service", target.MCVersion, now),
	}, identityMismatch
}

// --- verdict and transforms --------------------------------------------------

// analysis collects non-protocol flags gathered during inspection. It is
// local to one Check call so the report itself stays a plain data struct.
type analysis struct {
	crossEdition     bool
	marketplace      bool
	downgrade        bool
	cloneBackup      bool
	versionDecision  bool
	loaderMismatch   bool
	identityMismatch bool
	packWarning      bool
}

func (a *analysis) merge(b analysis) {
	a.crossEdition = a.crossEdition || b.crossEdition
	a.marketplace = a.marketplace || b.marketplace
	a.downgrade = a.downgrade || b.downgrade
	a.cloneBackup = a.cloneBackup || b.cloneBackup
	a.versionDecision = a.versionDecision || b.versionDecision
	a.loaderMismatch = a.loaderMismatch || b.loaderMismatch
	a.identityMismatch = a.identityMismatch || b.identityMismatch
	a.packWarning = a.packWarning || b.packWarning
}

var (
	hardGatingSubjects = map[string]bool{
		"archive": true, "edition": true, "data_version": true,
		"target_data_version": true, "loader": true, "dimensions": true,
	}
	softGatingSubjects = map[string]bool{
		"registries": true, "datapacks": true, "target_edition": true,
		"plugins": true, "resource_packs": true, "player_identity": true,
	}
)

func (a analysis) verdict(findings []Finding, transforms []Transform) string {
	for _, t := range transforms {
		if t.Status == StatusBlocked {
			return VerdictBlocked
		}
	}
	for _, f := range findings {
		if f.Confidence == Unknown && hardGatingSubjects[f.Subject] {
			return VerdictUnknown
		}
	}
	for _, t := range transforms {
		if t.Status == StatusRequiresDecision {
			return VerdictRequiresDecision
		}
	}
	for _, f := range findings {
		if f.Confidence == Unknown && softGatingSubjects[f.Subject] {
			return VerdictRequiresDecision
		}
	}
	return VerdictCompatible
}

func findTransformIndex(transforms []Transform, name string) int {
	for i, t := range transforms {
		if t.Name == name {
			return i
		}
	}
	return -1
}

func (a analysis) transforms() []Transform {
	var out []Transform
	add := func(t Transform) {
		if findTransformIndex(out, t.Name) >= 0 {
			return
		}
		if t.Reversibility == "" {
			t.Reversibility = "restore the previous immutable revision"
		}
		if t.Downtime == "" {
			t.Downtime = "server stop and start required if applied"
		}
		out = append(out, t)
	}
	unsupported := func(name string) Transform {
		return Transform{
			Name: name, Status: StatusUnsupported,
			Reversibility: "not applicable (never applied)",
			Downtime:      "none (never applied)",
			Reason:        MustUnsupported(name),
		}
	}

	switch {
	case a.crossEdition:
		add(Transform{Name: "edition_conversion", Status: StatusBlocked,
			Reversibility: "not applicable (never applied)", Downtime: "none (never applied)",
			Reason: MustUnsupported("edition_conversion")})
	case a.marketplace:
		add(unsupported("edition_conversion"))
	}
	if a.downgrade {
		add(Transform{Name: "in_place_downgrade", Status: StatusBlocked,
			Reversibility: "not applicable (never applied)", Downtime: "none (never applied)",
			Reason: MustUnsupported("in_place_downgrade")})
	}
	if a.cloneBackup {
		reason := "Clone the immutable revision before any version change; the original stays untouched."
		if a.downgrade {
			reason = "In-place downgrade is refused. Clone the revision, back it up, and restore an older immutable revision instead."
		}
		add(Transform{Name: "clone_and_backup", Status: StatusSupported,
			Reversibility: "restore the previous immutable revision", Downtime: "server stop and start required",
			Reason: reason})
	}
	if a.versionDecision {
		add(Transform{Name: "version_decision", Status: StatusRequiresDecision,
			Reversibility: "restore the previous immutable revision", Downtime: "server stop and start required if applied",
			Reason: "Target version capability is unknown; no upgrade or downgrade is attempted without an operator decision."})
	}
	if a.loaderMismatch {
		add(Transform{Name: "loader_review", Status: StatusRequiresDecision,
			Reversibility: "restore the previous immutable revision", Downtime: "server stop and start required if applied",
			Reason: "The archive loader and the target loader differ; loaders are never auto-swapped."})
	}
	if a.identityMismatch {
		add(Transform{Name: "player_identity_review", Status: StatusRequiresDecision,
			Reversibility: "restore the previous immutable revision", Downtime: "server stop and start required if applied",
			Reason: "Online/offline UUID schemes differ; player identities are never rewritten."})
	}
	if a.packWarning {
		add(Transform{Name: "datapack_review", Status: StatusRequiresDecision,
			Reversibility: "restore the previous immutable revision", Downtime: "server stop and start required if applied",
			Reason: "A datapack declares a pack format newer than the target version; no automatic repair is performed."})
	}
	for _, key := range unsupportedKeys {
		add(unsupported(key))
	}
	return out
}

// --- public engine API -------------------------------------------------------

// Check inspects a worldstore ZIP or a plain world directory and produces a
// read-only compatibility report. If the archive cannot be examined, affected
// findings are Unknown with a reason and no values are fabricated.
func (e *Engine) Check(ctx context.Context, archivePath string, target Target) (*Report, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	report := &Report{
		ServerID:        target.ServerID,
		TargetMCVersion: target.MCVersion,
		Unsupported:     unsupportedAll(),
	}

	world, err := openWorldFS(ctx, archivePath)
	if err != nil {
		if canceled := ctx.Err(); canceled != nil {
			return nil, canceled
		}
		reason := "Archive could not be examined: " + err.Error()
		for _, subject := range []string{"archive", "edition", "data_version", "loader", "dimensions", "datapacks"} {
			report.Findings = append(report.Findings,
				newFinding(subject, Unknown, "", reason, "on_disk_nbt", target.MCVersion, now))
		}
		var a analysis
		report.Transforms = a.transforms()
		report.Overall = a.verdict(report.Findings, report.Transforms)
		return report, nil
	}
	defer func() { _ = world.Close() }()
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var a analysis
	edition := inspectEdition(world, target, archivePath, now)
	a.merge(edition.analysis)
	report.Findings = append(report.Findings, edition.finding)
	report.Findings = append(report.Findings, edition.extra...)

	applicability := ""
	if edition.detectedEdition != "" {
		applicability = edition.detectedEdition
	}
	if v, ok := FactNested(edition.facts, "Version", "Id"); ok {
		applicability += "/" + fmt.Sprint(v)
	}
	dvFindings, dvAnalysis := inspectDataVersion(edition.facts, edition.detectedEdition, target, now)
	a.merge(dvAnalysis)
	report.Findings = append(report.Findings, dvFindings...)

	loaderFinding, loaderMismatch := inspectLoader(world, target, applicability, now)
	a.loaderMismatch = loaderMismatch
	report.Findings = append(report.Findings, loaderFinding)

	report.Findings = append(report.Findings, inspectDimensions(world, now)...)

	packFinding, packWarning := inspectDatapacks(world, target, now)
	a.packWarning = packWarning
	report.Findings = append(report.Findings, packFinding)

	metadataFindings, identityMismatch := inspectServerMetadata(target, now)
	a.identityMismatch = identityMismatch
	report.Findings = append(report.Findings, metadataFindings...)

	report.Transforms = a.transforms()
	report.Overall = a.verdict(report.Findings, report.Transforms)
	return report, nil
}

// DryRun returns the complete transform set for a report. It performs no file,
// archive, network or database operation and does not modify the report.
func (e *Engine) DryRun(ctx context.Context, report *Report) ([]Transform, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if report == nil {
		return nil, errors.New("worldlib: nil compatibility report")
	}
	out := make([]Transform, 0, len(report.Transforms)+len(unsupportedKeys))
	out = append(out, report.Transforms...)
	for _, key := range unsupportedKeys {
		i := findTransformIndex(out, key)
		if i < 0 {
			out = append(out, Transform{
				Name: key, Status: StatusUnsupported,
				Reversibility: "not applicable (never applied)",
				Downtime:      "none (never applied)",
				Reason:        MustUnsupported(key),
			})
			continue
		}
		if out[i].Reason == "" {
			out[i].Reason = MustUnsupported(key)
		}
		if out[i].Reversibility == "" {
			out[i].Reversibility = "not applicable (never applied)"
		}
		if out[i].Downtime == "" {
			out[i].Downtime = "none (never applied)"
		}
	}
	return out, nil
}
