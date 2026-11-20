// SPDX-License-Identifier: MPL-2.0

// Package serverarchive encodes validated, node-independent server data.
package serverarchive

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode"

	"github.com/athNdev/carbon-panel/internal/nodestorage"
	"gopkg.in/yaml.v3"
)

type Format string

const (
	ZIP     Format = "zip"
	TarGzip Format = "tar.gz"

	SchemaVersion      = 1
	MaxFiles           = 50_000
	MaxExpandedBytes   = 32 << 30
	maxManifestBytes   = 32 << 20
	maxPropertiesBytes = 1 << 20
	manifestName       = "manifest.json"
	dataPrefix         = "data/"
)

// Runtime is an allowlist. Never serialize the server row or its Docker and
// environment settings, which can contain secrets and deployment bindings.
type Runtime struct {
	MinecraftVersion      string `json:"minecraft_version"`
	ModLoader             string `json:"mod_loader"`
	LoaderVersion         string `json:"loader_version,omitempty"`
	MemoryMB              int    `json:"memory_mb"`
	MaxPlayers            int    `json:"max_players"`
	RequiresCustomRuntime bool   `json:"requires_custom_runtime,omitempty"`
	RequiresProviderSetup bool   `json:"requires_provider_setup,omitempty"`
}

type File struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion   int     `json:"schema_version"`
	ExporterVersion string  `json:"exporter_version"`
	Runtime         Runtime `json:"runtime"`
	Files           []File  `json:"files"`
}

type Archive struct {
	Format        Format
	Manifest      Manifest
	ExpandedBytes int64
}

// Write streams from the owning node. The caller must hold server lifecycle
// admission and have verified that the game and dependent writers are stopped.
func Write(ctx context.Context, source nodestorage.Accessor, dst io.Writer, format Format, runtime Runtime, exporterVersion string) (err error) {
	if err := validateRuntime(runtime, exporterVersion); err != nil {
		return err
	}

	var entry func(string, int64) (io.Writer, error)
	switch format {
	case ZIP:
		w := zip.NewWriter(dst)
		defer func() { err = errors.Join(err, w.Close()) }()
		entry = func(name string, _ int64) (io.Writer, error) {
			return w.Create(name)
		}
	case TarGzip:
		gz := gzip.NewWriter(dst)
		w := tar.NewWriter(gz)
		defer func() { err = errors.Join(err, w.Close(), gz.Close()) }()
		entry = func(name string, size int64) (io.Writer, error) {
			return w, w.WriteHeader(&tar.Header{Name: name, Mode: 0644, Size: size, Typeflag: tar.TypeReg, Format: tar.FormatPAX})
		}
	default:
		return fmt.Errorf("unsupported server archive format")
	}

	manifest := Manifest{
		SchemaVersion: SchemaVersion, ExporterVersion: exporterVersion,
		Runtime: runtime, Files: []File{},
	}
	seen := pathIndex{}
	var expanded int64
	var visit func(string) error
	visit = func(dir string) error {
		entries, err := source.ReadDir(dir)
		if err != nil {
			return err
		}
		for _, child := range entries {
			if err := ctx.Err(); err != nil {
				return err
			}
			rel := path.Join(dir, child.Name())
			if excluded(rel) {
				continue
			}
			if !portablePath(rel) {
				return fmt.Errorf("server data contains a path that cannot be archived portably: %q", rel)
			}
			if child.IsDir() {
				if err := visit(rel); err != nil {
					return err
				}
				continue
			}
			if !child.Type().IsRegular() {
				return fmt.Errorf("server data contains a link or special file: %q", rel)
			}
			if len(manifest.Files) >= MaxFiles {
				return fmt.Errorf("server archive exceeds the file count limit")
			}
			if err := seen.add(rel); err != nil {
				return err
			}
			record, err := writeFile(ctx, source, entry, rel, MaxExpandedBytes-expanded)
			if err != nil {
				return err
			}
			manifest.Files = append(manifest.Files, record)
			expanded += record.Size
		}
		return nil
	}
	if err := visit("."); err != nil {
		return err
	}
	body, err := json.Marshal(manifest)
	if err != nil {
		return err
	}
	if len(body) > maxManifestBytes {
		return fmt.Errorf("server archive manifest exceeds the size limit")
	}
	w, err := entry(manifestName, int64(len(body)))
	if err != nil {
		return err
	}
	_, err = w.Write(body)
	return err
}

func writeFile(
	ctx context.Context, source nodestorage.Accessor, entry func(string, int64) (io.Writer, error),
	rel string, remaining int64,
) (File, error) {
	handle, err := source.OpenRead(ctx, rel)
	if err != nil {
		return File{}, err
	}
	defer handle.Close()
	size := handle.Size()
	var r io.Reader = handle
	if isManagedSettings(rel) {
		if size > maxPropertiesBytes {
			return File{}, fmt.Errorf("managed settings exceed the portability limit")
		}
		data, err := io.ReadAll(io.LimitReader(handle, maxPropertiesBytes+1))
		if err != nil {
			return File{}, err
		}
		if int64(len(data)) != size {
			return File{}, fmt.Errorf("managed settings changed during export")
		}
		safe, err := portableSettings(rel, data)
		if err != nil {
			return File{}, err
		}
		r, size = bytes.NewReader(safe), int64(len(safe))
	}
	if size < 0 || size > remaining {
		return File{}, fmt.Errorf("server archive exceeds the expanded size limit")
	}
	dst, err := entry(dataPrefix+rel, size)
	if err != nil {
		return File{}, err
	}
	hash := sha256.New()
	if err := copySized(ctx, io.MultiWriter(dst, hash), r, size); err != nil {
		return File{}, fmt.Errorf("copy server data %q: %w", rel, err)
	}
	return File{Path: rel, Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}

// Inspect reads and checks the complete archive without writing destination data.
func Inspect(ctx context.Context, f *os.File) (Archive, error) {
	return scan(ctx, f, nil)
}

// CopyTo rechecks the archive while streaming into a newly allocated, private
// directory. The caller owns rollback of that directory on any error.
func CopyTo(ctx context.Context, f *os.File, verified Archive, destination nodestorage.Accessor, directory string) error {
	if directory != "." && (!portablePath(directory) || path.Base(directory) != directory) {
		return fmt.Errorf("invalid destination directory")
	}
	if verified.Manifest.SchemaVersion != SchemaVersion {
		return fmt.Errorf("server archive was not inspected")
	}
	expected := make(map[string]File, len(verified.Manifest.Files))
	for _, file := range verified.Manifest.Files {
		expected[file.Path] = file
	}
	observed, err := scan(ctx, f, func(file File, contents io.Reader) error {
		old, ok := expected[file.Path]
		if !ok || old.Size != file.Size {
			return fmt.Errorf("server archive changed after inspection")
		}
		_, err := nodestorage.WriteFrom(ctx, destination, path.Join(directory, file.Path), contents, max(file.Size, 1))
		return err
	})
	if err != nil {
		return err
	}
	if observed.Format != verified.Format || observed.Manifest.Runtime != verified.Manifest.Runtime ||
		observed.Manifest.ExporterVersion != verified.Manifest.ExporterVersion ||
		len(observed.Manifest.Files) != len(expected) {
		return fmt.Errorf("server archive changed after inspection")
	}
	for _, file := range observed.Manifest.Files {
		if expected[file.Path] != file {
			return fmt.Errorf("server archive changed after inspection")
		}
	}
	return nil
}

func scan(ctx context.Context, f *os.File, consume func(File, io.Reader) error) (Archive, error) {
	var result Archive
	seen := pathIndex{}
	found := make(map[string]File)
	manifestRead := false

	readEntry := func(name string, size int64, r io.Reader) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == manifestName {
			if manifestRead || size < 0 || size > maxManifestBytes {
				return fmt.Errorf("invalid or duplicate server archive manifest")
			}
			var body bytes.Buffer
			if err := copySized(ctx, &body, r, size); err != nil {
				return err
			}
			decoder := json.NewDecoder(&body)
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&result.Manifest); err != nil {
				return fmt.Errorf("invalid server archive manifest: %w", err)
			}
			if decoder.Decode(new(any)) != io.EOF {
				return fmt.Errorf("unexpected data after server archive manifest")
			}
			manifestRead = true
			return nil
		}

		rel, ok := strings.CutPrefix(name, dataPrefix)
		if !ok || !portablePath(rel) || excluded(rel) {
			return fmt.Errorf("invalid server archive file path %q", name)
		}
		if len(found) >= MaxFiles || size < 0 || size > MaxExpandedBytes-result.ExpandedBytes {
			return fmt.Errorf("server archive exceeds the file count or expanded size limit")
		}
		if err := seen.add(rel); err != nil {
			return err
		}

		hash := sha256.New()
		record := File{Path: rel, Size: size}
		if isManagedSettings(rel) {
			if size > maxPropertiesBytes {
				return fmt.Errorf("managed settings exceed the portability limit")
			}
			var body bytes.Buffer
			if err := copySized(ctx, &body, r, size); err != nil {
				return err
			}
			safe, err := portableSettings(rel, body.Bytes())
			if err != nil {
				return err
			}
			if !bytes.Equal(body.Bytes(), safe) {
				return fmt.Errorf("archive contains nonportable or credential-bearing settings")
			}
			r = bytes.NewReader(body.Bytes())
		}

		if consume == nil {
			if err := copySized(ctx, hash, r, size); err != nil {
				return err
			}
		} else {
			contents := &io.LimitedReader{R: io.TeeReader(contextReader{context: ctx, r: r}, hash), N: size}
			if err := consume(record, contents); err != nil {
				return err
			}
			if contents.N != 0 {
				return io.ErrUnexpectedEOF
			}
			if err := copySized(ctx, io.Discard, r, 0); err != nil {
				return err
			}
		}
		record.SHA256 = hex.EncodeToString(hash.Sum(nil))
		found[rel] = record
		result.ExpandedBytes += size
		return nil
	}

	format, err := readContainer(ctx, f, readEntry)
	if err != nil {
		return Archive{}, err
	}
	result.Format = format
	if !manifestRead || result.Manifest.SchemaVersion != SchemaVersion {
		return Archive{}, fmt.Errorf("missing or unsupported Carbon Panel server archive schema")
	}
	if err := validateRuntime(result.Manifest.Runtime, result.Manifest.ExporterVersion); err != nil {
		return Archive{}, err
	}
	if result.Manifest.Files == nil || len(result.Manifest.Files) != len(found) {
		return Archive{}, fmt.Errorf("server archive contents do not match the manifest")
	}
	for _, file := range result.Manifest.Files {
		if expected, ok := found[file.Path]; !ok || expected != file {
			return Archive{}, fmt.Errorf("server archive checksum or size mismatch for %q", file.Path)
		}
		delete(found, file.Path)
	}
	return result, nil
}

func readContainer(ctx context.Context, f *os.File, entry func(string, int64, io.Reader) error) (Format, error) {
	var prefix [4]byte
	if _, err := f.ReadAt(prefix[:], 0); err != nil {
		return "", fmt.Errorf("read server archive header: %w", err)
	}
	switch {
	case prefix == [4]byte{'P', 'K', 3, 4}:
		stat, err := f.Stat()
		if err != nil {
			return "", err
		}
		if err := checkZIPDirectory(f, stat.Size()); err != nil {
			return "", err
		}
		archive, err := zip.NewReader(f, stat.Size())
		if err != nil {
			return "", err
		}
		if len(archive.File) > MaxFiles+1 {
			return "", fmt.Errorf("server archive exceeds the file count limit")
		}
		for _, member := range archive.File {
			if !member.Mode().IsRegular() || member.UncompressedSize64 > MaxExpandedBytes {
				return "", fmt.Errorf("invalid server archive member %q", member.Name)
			}
			r, err := member.Open()
			if err != nil {
				return "", err
			}
			if err := errors.Join(entry(member.Name, int64(member.UncompressedSize64), r), r.Close()); err != nil {
				return "", err
			}
		}
		return ZIP, nil
	case prefix[0] == 0x1f && prefix[1] == 0x8b:
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		raw := bufio.NewReader(f)
		gz, err := gzip.NewReader(raw)
		if err != nil {
			return "", err
		}
		defer gz.Close()
		gz.Multistream(false)
		archive := tar.NewReader(gz)
		for {
			member, err := archive.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				return "", err
			}
			if member.Typeflag != tar.TypeReg || !onlyPortablePAX(member.PAXRecords) {
				return "", fmt.Errorf("invalid server archive member %q", member.Name)
			}
			if err := entry(member.Name, member.Size, archive); err != nil {
				return "", err
			}
		}
		if err := copySized(ctx, io.Discard, gz, 0); err != nil {
			return "", fmt.Errorf("unexpected or corrupt server archive data after TAR: %w", err)
		}
		if _, err := raw.Peek(1); err != io.EOF {
			return "", fmt.Errorf("unexpected data after gzip stream")
		}
		return TarGzip, nil
	default:
		return "", fmt.Errorf("expected a Carbon Panel ZIP or tar.gz archive")
	}
}

// The native writer emits no archive comment. Check the directory envelope
// before archive/zip allocates a file table from uploaded metadata.
func checkZIPDirectory(f *os.File, size int64) error {
	const endSize = 22
	const maxDirectoryBytes = (MaxFiles + 1) * 1024

	var end [endSize]byte
	if size < endSize {
		return fmt.Errorf("server ZIP is missing its directory")
	}
	if _, err := f.ReadAt(end[:], size-endSize); err != nil {
		return err
	}
	u16 := binary.LittleEndian.Uint16
	u32 := binary.LittleEndian.Uint32
	u64 := binary.LittleEndian.Uint64
	if u32(end[:]) != 0x06054b50 || u16(end[4:]) != 0 || u16(end[6:]) != 0 || u16(end[20:]) != 0 {
		return fmt.Errorf("invalid server ZIP directory")
	}
	count := uint64(u16(end[10:]))
	length := uint64(u32(end[12:]))
	offset := uint64(u32(end[16:]))
	limit := uint64(size - endSize)
	if count == 0xffff || length == 0xffffffff || offset == 0xffffffff {
		var locator [20]byte
		var record [56]byte
		if limit < uint64(len(record)+len(locator)) {
			return fmt.Errorf("invalid server ZIP64 directory")
		}
		limit -= uint64(len(locator))
		if _, err := f.ReadAt(locator[:], int64(limit)); err != nil {
			return err
		}
		if u32(locator[:]) != 0x07064b50 || u32(locator[4:]) != 0 || u32(locator[16:]) != 1 {
			return fmt.Errorf("invalid server ZIP64 directory")
		}
		limit -= uint64(len(record))
		if u64(locator[8:]) != limit {
			return fmt.Errorf("invalid server ZIP64 directory")
		}
		if _, err := f.ReadAt(record[:], int64(limit)); err != nil {
			return err
		}
		if u32(record[:]) != 0x06064b50 || u64(record[4:]) != 44 ||
			u32(record[16:]) != 0 || u32(record[20:]) != 0 || u64(record[24:]) != u64(record[32:]) {
			return fmt.Errorf("invalid server ZIP64 directory")
		}
		count, length, offset = u64(record[32:]), u64(record[40:]), u64(record[48:])
	} else if uint64(u16(end[8:])) != count {
		return fmt.Errorf("invalid server ZIP directory")
	}
	if count == 0 || count > MaxFiles+1 || length > maxDirectoryBytes ||
		offset > limit || length != limit-offset {
		return fmt.Errorf("server ZIP directory is inconsistent or exceeds archive limits")
	}
	return nil
}

// PAX may be required to store a long or Unicode path. Other extensions,
// including sparse-file descriptions, are outside the native format.
func onlyPortablePAX(records map[string]string) bool {
	for k := range records {
		if k != "path" && k != "size" {
			return false
		}
	}
	return true
}

type contextReader struct {
	context context.Context
	r       io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.context.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func copySized(ctx context.Context, dst io.Writer, r io.Reader, size int64) error {
	source := contextReader{context: ctx, r: r}
	n, err := io.Copy(dst, io.LimitReader(source, size+1))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("archive entry size differs from declared size")
	}
	return nil
}

func validateRuntime(r Runtime, exporter string) error {
	if !versionField(exporter) || !versionField(r.MinecraftVersion) || !versionField(r.ModLoader) ||
		(r.LoaderVersion != "" && !versionField(r.LoaderVersion)) || r.MemoryMB <= 0 ||
		r.MemoryMB > 1<<20 || r.MaxPlayers <= 0 || r.MaxPlayers > 1<<16 {
		return fmt.Errorf("invalid server archive runtime configuration")
	}
	return nil
}

func versionField(s string) bool {
	return len(s) > 0 && len(s) <= 128 && strings.IndexFunc(s, func(c rune) bool {
		return !strings.ContainsRune("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789.-_+", c)
	}) == -1
}

func portablePath(rel string) bool {
	if rel == "." || !fs.ValidPath(rel) || len(rel) > 512 || strings.Count(rel, "/") >= 32 {
		return false
	}
	for _, component := range strings.Split(rel, "/") {
		if len(component) > 255 || strings.HasSuffix(component, ".") ||
			strings.HasSuffix(component, " ") || strings.ContainsAny(component, "<>:\"\\|?*") ||
			strings.IndexFunc(component, unicode.IsControl) >= 0 {
			return false
		}
		stem, _, _ := strings.Cut(strings.ToUpper(component), ".")
		stem = strings.TrimRight(stem, " ")
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return false
		}
		if suffix, ok := strings.CutPrefix(stem, "COM"); ok && deviceIndex(suffix) {
			return false
		}
		if suffix, ok := strings.CutPrefix(stem, "LPT"); ok && deviceIndex(suffix) {
			return false
		}
	}
	return true
}

func deviceIndex(s string) bool {
	switch s {
	case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
		return true
	default:
		return false
	}
}

type indexedPath struct {
	name      string
	directory bool
}

type pathIndex map[string]indexedPath

func (seen pathIndex) add(rel string) error {
	for name := rel; name != "."; name = path.Dir(name) {
		key := strings.ToUpper(name)
		current := indexedPath{name: name, directory: name != rel}
		if before, ok := seen[key]; ok {
			if current != before || !current.directory {
				return fmt.Errorf("duplicate or conflicting server archive path %q", rel)
			}
			break
		}
		if len(seen) >= MaxFiles*4 {
			return fmt.Errorf("server archive exceeds the path count limit")
		}
		seen[key] = current
	}
	return nil
}

func excluded(rel string) bool {
	if nodestorage.IsWriteStaging(rel) {
		return true
	}
	for _, component := range strings.Split(strings.ToLower(rel), "/") {
		switch component {
		case ".git", ".ssh", ".env", "logs", "crash-reports", "session.lock", "forwarding.secret", "secrets", "credentials":
			return true
		}
		if strings.HasPrefix(component, ".env.") || strings.HasPrefix(component, ".carbon-") {
			return true
		}
		switch path.Ext(component) {
		case ".key", ".pem", ".p12", ".pfx", ".jks", ".secret":
			return true
		}
	}
	return false
}

// Copy only recognized game settings. Java properties allow escaped keys and
// logical line continuation, so require simple literal keys and omit continued
// records. Drop comments to keep operator notes out of the archive.
func portableProperties(data []byte) ([]byte, error) {
	var safe bytes.Buffer
	continued := false
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		trimmed := bytes.TrimRight(line, "\r\n")
		slashes := len(trimmed) - len(bytes.TrimRight(trimmed, "\\"))
		wasContinued := continued
		continued = slashes%2 != 0
		if wasContinued || continued || bytes.IndexByte(trimmed, '\r') >= 0 {
			continue
		}
		key, value, ok := bytes.Cut(bytes.TrimLeft(trimmed, " \t\f"), []byte("="))
		if !ok {
			continue
		}
		if string(key) == "level-name" {
			name := string(value)
			if !portablePath(name) || path.Base(name) != name {
				return nil, fmt.Errorf("level-name is not a portable world directory name")
			}
		}
		switch string(key) {
		case "allow-flight", "allow-nether", "broadcast-console-to-ops", "broadcast-rcon-to-ops",
			"debug", "difficulty", "enforce-secure-profile", "enforce-whitelist", "entity-broadcast-range-percentage",
			"force-gamemode", "function-permission-level", "gamemode", "generate-structures", "generator-settings",
			"hardcore", "hide-online-players", "initial-disabled-packs", "initial-enabled-packs", "level-name",
			"level-seed", "level-type", "max-chained-neighbor-updates", "max-players", "max-tick-time",
			"max-world-size", "motd", "network-compression-threshold", "op-permission-level",
			"pause-when-empty-seconds", "player-idle-timeout", "prevent-proxy-connections", "pvp", "rate-limit",
			"region-file-compression", "require-resource-pack", "simulation-distance", "spawn-animals",
			"spawn-monsters", "spawn-npcs", "spawn-protection", "sync-chunk-writes", "text-filtering-version",
			"use-native-transport", "view-distance", "white-list":
			safe.Write(line)
		}
	}
	return safe.Bytes(), nil
}

func isManagedSettings(rel string) bool {
	switch strings.ToLower(rel) {
	case "server.properties", "paper.yml", "config/paper-global.yml":
		return true
	default:
		return false
	}
}

func portableSettings(rel string, data []byte) ([]byte, error) {
	switch strings.ToLower(rel) {
	case "server.properties":
		return portableProperties(data)
	case "paper.yml":
		return withoutPaperForwarding(data, "settings", "velocity-support")
	case "config/paper-global.yml":
		return withoutPaperForwarding(data, "proxies", "velocity")
	default:
		return data, nil
	}
}

func withoutPaperForwarding(data []byte, section, key string) ([]byte, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var root map[string]any
	if err := decoder.Decode(&root); err != nil {
		if err == io.EOF {
			return data, nil
		}
		return nil, fmt.Errorf("could not validate Paper settings")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, fmt.Errorf("unexpected data after Paper settings")
	}
	config, _ := root[section].(map[string]any)
	if config == nil {
		return data, nil
	}
	if _, exists := config[key]; !exists {
		return data, nil
	}
	delete(config, key)
	return yaml.Marshal(root)
}
