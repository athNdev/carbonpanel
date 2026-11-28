// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package minecraft

import (
	"archive/zip"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	models "github.com/athNdev/carbon-panel/internal/db"
	"gopkg.in/yaml.v3"
)

// ModCompatStatus classifies one installed content file against a target
// Minecraft version.
type ModCompatStatus string

const (
	// ModCompatCompatible: the file declares compatibility with the target.
	ModCompatCompatible ModCompatStatus = "compatible"
	// ModCompatIncompatible: the file declares a constraint the target fails.
	ModCompatIncompatible ModCompatStatus = "incompatible"
	// ModCompatUnknown: the file carries no readable constraint (unreadable
	// archive, foreign metadata, or none declared). Unknown never blocks a
	// version change.
	ModCompatUnknown ModCompatStatus = "unknown"
)

// ModCompatReport is the per-file result of a compatibility scan.
type ModCompatReport struct {
	// File is the base name of the scanned archive.
	File string
	// Constraint is the declared Minecraft constraint, rendered the way the
	// file declares it ("" when none was found).
	Constraint string
	// Status classifies the file against the target version.
	Status ModCompatStatus
	// Reason explains an incompatible verdict in display form.
	Reason string
}

// contentFamily groups loaders by the jar metadata their content carries.
type contentFamily string

// declaredConstraint keeps the constraint as declared for diagnostics while
// retaining the metadata dialect used to interpret it.
type declaredConstraint struct {
	display string
	match   string
}

const (
	familyFabric contentFamily = "fabric" // fabric.mod.json / quilt.mod.json depends
	familyPlugin contentFamily = "plugin" // plugin.yml / paper-plugin.yml api-version
	familyOther  contentFamily = "other"  // no metadata the panel reads
)

// contentFamilyFor maps a loader to the jar metadata family its installed
// content uses. Loaders outside the two families report no readable
// constraint, which callers must treat as unknown, never incompatible.
func contentFamilyFor(loader models.ModLoader) contentFamily {
	switch loader {
	case models.ModLoaderFabric, models.ModLoaderQuilt:
		return familyFabric
	case models.ModLoaderBukkit, models.ModLoaderSpigot, models.ModLoaderPaper,
		models.ModLoaderPurpur, models.ModLoaderPufferfish, models.ModLoaderFolia:
		return familyPlugin
	default:
		return familyOther
	}
}

// ScanContentCompatibility judges the jars installed for a server's loader
// family against targetVersion. Fabric/Quilt mods declare a "minecraft"
// constraint in fabric.mod.json / quilt.mod.json depends; Bukkit-family
// plugins declare plugin.yml or paper-plugin.yml api-version, which acts as a
// lower bound. Loaders without a content directory (vanilla, modpack
// platforms) and missing directories report no files. Unknown mod loaders on
// compatibility-sensitive mod files fail closed instead of being silently
// skipped. Results are sorted by file name so reports are deterministic.
func ScanContentCompatibility(dataRoot string, loader models.ModLoader, targetVersion string) []ModCompatReport {
	info := GetModLoaderInfo(loader)
	if info.Description == "Unknown mod loader" {
		// The loader is unknown, so GetModLoaderInfo cannot name a content
		// directory. Probe the conventional roots instead of skipping them:
		// jars there are content the panel cannot verify, so they fail closed.
		var reports []ModCompatReport
		for _, directory := range []string{"mods", "plugins"} {
			jars, err := jarNames(filepath.Join(dataRoot, directory))
			if err != nil {
				continue
			}
			for _, name := range jars {
				reports = append(reports, ModCompatReport{
					File:   name,
					Status: ModCompatIncompatible,
					Reason: "unknown mod loader: compatibility cannot be verified",
				})
			}
		}
		slices.SortStableFunc(reports, func(a, b ModCompatReport) int {
			return strings.Compare(a.File, b.File)
		})
		return reports
	}
	family := contentFamilyFor(loader)
	if family == familyOther {
		return nil
	}
	if info.ModsDirectory == "" {
		return nil
	}
	dir := filepath.Join(dataRoot, info.ModsDirectory)
	names, err := jarNames(dir)
	if err != nil {
		// No installed content (or an unreadable directory): nothing to judge.
		return nil
	}
	reports := make([]ModCompatReport, 0, len(names))
	for _, name := range names {
		reports = append(reports, scanContentJar(filepath.Join(dir, name), name, family, targetVersion))
	}
	return reports
}

// ScanDatapackCompatibility judges installed datapack archives against
// targetVersion. A pack whose declared minimum format exceeds the format the
// target release supports is marked incompatible by the game — that is a
// downgrade (or a target too old for the pack), so it is refused. Packs that
// declare no usable format report unknown.
func ScanDatapackCompatibility(datapacksDir, targetVersion string) []ModCompatReport {
	entries, err := readDirNames(datapacksDir, ".zip")
	if err != nil {
		return nil
	}
	reports := make([]ModCompatReport, 0, len(entries))
	for _, name := range entries {
		report := ModCompatReport{File: name, Status: ModCompatUnknown}
		pack, err := ReadPackFormat(filepath.Join(datapacksDir, name))
		if err != nil || !pack.Known() {
			reports = append(reports, report)
			continue
		}
		report.Constraint = pack.String()
		if warning := PackFormatWarning(pack, targetVersion); warning != "" {
			report.Status = ModCompatIncompatible
			report.Reason = warning
		} else {
			report.Status = ModCompatCompatible
		}
		reports = append(reports, report)
	}
	return reports
}

func jarNames(dir string) ([]string, error) {
	return readDirNames(dir, ".jar")
}

func readDirNames(dir, ext string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), ext) {
			continue
		}
		names = append(names, e.Name())
	}
	slices.Sort(names)
	return names, nil
}

func scanContentJar(path, name string, family contentFamily, targetVersion string) ModCompatReport {
	report := ModCompatReport{File: name, Status: ModCompatUnknown}
	constraint, found, err := readJarConstraint(path, family)
	if err != nil || !found {
		// An unreadable archive or a file that declares nothing the panel
		// understands is unknown, never a blocker.
		return report
	}
	report.Constraint = constraint.display
	ok, understood := MinecraftConstraintMatch(constraint.match, targetVersion)
	if !understood {
		return report
	}
	if ok {
		report.Status = ModCompatCompatible
		return report
	}
	report.Status = ModCompatIncompatible
	report.Reason = "requires " + constraint.display
	return report
}

// readJarConstraint extracts the Minecraft version constraint declared by a
// content archive. It reads the metadata the loader family carries and
// reports found=false when the archive declares nothing readable.
func readJarConstraint(path string, family contentFamily) (constraint declaredConstraint, found bool, err error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return declaredConstraint{}, false, err
	}
	defer func() { _ = r.Close() }()

	switch family {
	case familyFabric:
		if c, ok := readFabricConstraint(&r.Reader); ok {
			return declaredConstraint{display: c, match: c}, true, nil
		}
		if c, ok := readQuiltConstraint(&r.Reader); ok {
			return declaredConstraint{display: c, match: quiltConstraintForMatching(c)}, true, nil
		}
	case familyPlugin:
		if c, ok := readPluginAPIVersion(&r.Reader, "plugin.yml"); ok {
			return declaredConstraint{display: c, match: c}, true, nil
		}
		if c, ok := readPluginAPIVersion(&r.Reader, "paper-plugin.yml"); ok {
			return declaredConstraint{display: c, match: c}, true, nil
		}
	}
	return declaredConstraint{}, false, nil
}

// readFabricConstraint reads the "minecraft" entry of fabric.mod.json
// depends. The value is a constraint string or a list of alternatives.
func readFabricConstraint(r *zip.Reader) (string, bool) {
	for _, f := range r.File {
		if f.FileInfo().IsDir() || jarMemberName(f.Name) != "fabric.mod.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", false
		}
		var meta struct {
			Depends map[string]any `json:"depends"`
		}
		decodeErr := json.NewDecoder(rc).Decode(&meta)
		_ = rc.Close()
		if decodeErr != nil {
			return "", false
		}
		raw, ok := meta.Depends["minecraft"]
		if !ok {
			return "", false
		}
		if c, ok := anyConstraintString(raw, " or "); ok {
			return c, true
		}
		return "", false
	}
	return "", false
}

// readQuiltConstraint reads the minecraft entry of quilt.mod.json depends.
// Quilt dependency entries may be string identifiers or objects with id and
// versions, so each entry is decoded independently and a malformed entry does
// not invalidate the rest of the descriptor.
func readQuiltConstraint(r *zip.Reader) (string, bool) {
	for _, f := range r.File {
		if f.FileInfo().IsDir() || jarMemberName(f.Name) != "quilt.mod.json" {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", false
		}
		var meta struct {
			QuiltLoader struct {
				Depends []json.RawMessage `json:"depends"`
			} `json:"quilt_loader"`
		}
		decodeErr := json.NewDecoder(rc).Decode(&meta)
		_ = rc.Close()
		if decodeErr != nil {
			return "", false
		}
		for _, raw := range meta.QuiltLoader.Depends {
			var identifier string
			if err := json.Unmarshal(raw, &identifier); err == nil {
				// A string dependency carries no version constraint.
				continue
			}
			var dep map[string]any
			if err := json.Unmarshal(raw, &dep); err != nil {
				continue
			}
			id, _ := dep["id"].(string)
			if !strings.EqualFold(strings.TrimSpace(id), "minecraft") {
				continue
			}
			if c, ok := anyConstraintString(dep["versions"], " or "); ok {
				return c, true
			}
		}
		return "", false
	}
	return "", false
}

// readPluginAPIVersion reads plugin.yml / paper-plugin.yml api-version. The
// Bukkit api-version is a minimum: the plugin is refused on older releases
// and generally accepted on newer ones, so the constraint is a >= bound.
func readPluginAPIVersion(r *zip.Reader, member string) (string, bool) {
	for _, f := range r.File {
		if f.FileInfo().IsDir() || jarMemberName(f.Name) != member {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", false
		}
		var meta struct {
			APIVersion any `yaml:"api-version"`
		}
		decodeErr := yaml.NewDecoder(rc).Decode(&meta)
		_ = rc.Close()
		if decodeErr != nil {
			return "", false
		}
		api, ok := scalarString(meta.APIVersion)
		if !ok || api == "" {
			return "", false
		}
		return ">=" + api, true
	}
	return "", false
}

// jarMemberName normalizes a zip member path for root-level matching.
func jarMemberName(name string) string {
	return strings.TrimPrefix(filepath.ToSlash(name), "./")
}

// quiltConstraintForMatching converts Quilt's bare-version requirement into
// the equivalent caret range. Explicit operators (including "=") and wildcard
// requirements retain their declared meaning.
func quiltConstraintForMatching(constraint string) string {
	fields := strings.Fields(constraint)
	for i, field := range fields {
		switch {
		case field == "",
			strings.EqualFold(field, "or"),
			strings.HasPrefix(field, ">"),
			strings.HasPrefix(field, "<"),
			strings.HasPrefix(field, "="),
			strings.HasPrefix(field, "~"),
			strings.HasPrefix(field, "^"),
			strings.ContainsAny(field, "*xX"):
			continue
		}
		if bound := boundVersion(field); bound != "" {
			fields[i] = "^" + bound
		}
	}
	return strings.Join(fields, " ")
}

// anyConstraintString renders a declared constraint value: a plain string or
// a list of alternatives joined with joiner. Ambiguous or unparseable values
// are rejected so callers fail closed instead of treating them as satisfied.
func anyConstraintString(raw any, joiner string) (string, bool) {
	switch v := raw.(type) {
	case string:
		s := strings.TrimSpace(v)
		if s == "" {
			return "", false
		}
		return s, true
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			s, ok := scalarString(item)
			if !ok || s == "" {
				return "", false
			}
			parts = append(parts, s)
		}
		if len(parts) == 0 {
			return "", false
		}
		return strings.Join(parts, joiner), true
	default:
		return "", false
	}
}

// scalarString coerces a declared version scalar (string, YAML number, or
// JSON number) to its display form.
func scalarString(raw any) (string, bool) {
	switch v := raw.(type) {
	case string:
		return strings.TrimSpace(v), true
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), true
	case int:
		return strconv.Itoa(v), true
	case int64:
		return strconv.FormatInt(v, 10), true
	case json.Number:
		return v.String(), true
	default:
		return "", false
	}
}

// MinecraftConstraintMatch reports whether constraint admits version, using a
// deliberately conservative grammar: "or"/"||"-separated alternatives, each a
// whitespace- (or "&&"-) separated list of simple bounds. A bound is a
// comparison (>, >=, <, <=, =), a "~"/"^" pinned range, a "1.21.x"-style
// wildcard tail, or an exact dotted version. Numeric segments compare
// numerically. Anything outside the grammar — or a non-release target such
// as a snapshot — reports understood=false so callers treat the file as
// unknown instead of guessing. If no alternative matches, any unknown
// alternative keeps the result unknown rather than proving incompatibility.
func MinecraftConstraintMatch(constraint, version string) (ok bool, understood bool) {
	c := strings.TrimSpace(constraint)
	if c == "" || c == "*" || c == "x" || c == "X" {
		return true, true
	}
	target := normalizeVersion(version)
	if target == "" {
		// A non-release target (snapshot, "LATEST") cannot be judged.
		return false, false
	}
	expr := strings.ReplaceAll(c, "||", " or ")
	anyAlt := false
	understoodAny := false
	unknownAny := false
	for _, alt := range strings.Split(expr, " or ") {
		okAlt, understoodAlt := matchSingleConstraint(alt, target)
		if !understoodAlt {
			unknownAny = true
			continue
		}
		understoodAny = true
		if okAlt {
			anyAlt = true
		}
	}
	if anyAlt {
		return true, true
	}
	if unknownAny {
		return false, false
	}
	return false, understoodAny
}

// matchSingleConstraint ANDs a whitespace-separated list of bounds, treating
// a bare "or" token as an alternative boundary. One unparsable bound makes
// that whole alternative unknown; a known match still succeeds, otherwise an
// unknown alternative keeps the expression unknown.
func matchSingleConstraint(expr, target string) (bool, bool) {
	anyAlt, understoodAny, unknownAny := false, false, false
	currentOK, currentUnderstood, sawBound := true, true, false
	flush := func() {
		if sawBound {
			if currentUnderstood {
				understoodAny = true
				if currentOK {
					anyAlt = true
				}
			} else {
				unknownAny = true
			}
		}
		currentOK, currentUnderstood, sawBound = true, true, false
	}
	for _, part := range strings.Fields(strings.ReplaceAll(expr, "&&", " ")) {
		if strings.EqualFold(part, "or") {
			flush()
			continue
		}
		sawBound = true
		ok, understood := matchBound(part, target)
		if !understood {
			currentUnderstood = false
			continue
		}
		if !ok {
			currentOK = false
		}
	}
	flush()
	if anyAlt {
		return true, true
	}
	if unknownAny {
		return false, false
	}
	return false, understoodAny
}

// boundVersion normalizes the version side of a bound. A trailing "-" is a
// quilt convention ("1.21-" = the 1.21 line) and carries no numeric meaning.
func boundVersion(raw string) string {
	return normalizeVersion(strings.TrimRight(strings.TrimSpace(raw), "-"))
}

// matchBound judges one bound against the padded target version.
func matchBound(expr, target string) (bool, bool) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return false, false
	}
	switch {
	case strings.HasPrefix(expr, ">="), strings.HasPrefix(expr, "<="), strings.HasPrefix(expr, "=="):
		op, bound := expr[:2], boundVersion(expr[2:])
		if bound == "" {
			return false, false
		}
		cmp := compareVersions(target, bound)
		switch op {
		case ">=":
			return cmp >= 0, true
		case "<=":
			return cmp <= 0, true
		default:
			return cmp == 0, true
		}
	case strings.HasPrefix(expr, ">"), strings.HasPrefix(expr, "<"),
		strings.HasPrefix(expr, "="), strings.HasPrefix(expr, "~"), strings.HasPrefix(expr, "^"):
		op, bound := expr[:1], boundVersion(expr[1:])
		if bound == "" {
			return false, false
		}
		cmp := compareVersions(target, bound)
		switch op {
		case ">":
			return cmp > 0, true
		case "<":
			return cmp < 0, true
		case "=":
			return cmp == 0, true
		case "~":
			// ~1.21.1: the same minor line, at or above the floor.
			return cmp >= 0 && compareVersions(target, bumpSegment(bound, 1)) < 0, true
		default: // "^"
			// ^1.21.1: the same major line, at or above the floor.
			return cmp >= 0 && compareVersions(target, bumpSegment(bound, 0)) < 0, true
		}
	}
	if strings.ContainsAny(expr, "*xX") {
		return matchWildcardTail(expr, target)
	}
	bound := normalizeVersion(expr)
	if bound == "" {
		return false, false
	}
	return compareVersions(target, bound) == 0, true
}

// matchWildcardTail admits any target that extends the numeric prefix of a
// "1.21.x" / "1.21.*" / "1.x" constraint.
func matchWildcardTail(expr, target string) (bool, bool) {
	segs := strings.Split(strings.ToLower(strings.TrimSpace(expr)), ".")
	tsegs := strings.Split(target, ".")
	for i, s := range segs {
		if s == "*" || s == "x" {
			return true, true
		}
		if i >= len(tsegs) || tsegs[i] != s {
			return false, true
		}
	}
	return true, true
}

// bumpSegment returns v with segment idx incremented and every later segment
// zeroed ("1.21.1", idx 1 -> "1.22.0").
func bumpSegment(v string, idx int) string {
	segs := strings.Split(v, ".")
	if idx < 0 || idx >= len(segs) {
		return v
	}
	n, err := strconv.Atoi(segs[idx])
	if err != nil {
		return v
	}
	segs[idx] = strconv.Itoa(n + 1)
	for i := idx + 1; i < len(segs); i++ {
		segs[i] = "0"
	}
	return strings.Join(segs, ".")
}
