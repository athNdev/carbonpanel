// SPDX-License-Identifier: MPL-2.0
// This Source Code Form is subject to the terms of the Mozilla Public
// License, v. 2.0. If a copy of the MPL was not distributed with this
// file, You can obtain one at https://mozilla.org/MPL/2.0/.

package docker

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/athNdev/carbon-panel/internal/db"
	v1 "github.com/athNdev/carbon-panel/pkg/proto/carbonpanel/v1"
	"github.com/moby/moby/api/types/container"
)

// ErrRuntimeIncompatible reports a container configuration that cannot boot the
// configured image. It is raised from the fully-resolved Docker configuration,
// before any daemon mutation, so an unsupported combination never replaces a
// running container or leaves a half-created one behind.
var ErrRuntimeIncompatible = errors.New("container configuration is incompatible with this image or runtime")

// RuntimeIssueSeverity orders issues by whether they block a boot.
type RuntimeIssueSeverity string

const (
	// RuntimeFatal issues mean the container cannot reach a playable state as
	// configured. They stop creation.
	RuntimeFatal RuntimeIssueSeverity = "fatal"
	// RuntimeWarning issues mean the configuration is weaker or more fragile
	// than the default, but the image can still boot. They are reported and
	// never stop creation.
	RuntimeWarning RuntimeIssueSeverity = "warning"
)

// RuntimeIssueCode is a stable machine-readable identifier for a compatibility
// finding. It is part of the operator-facing diagnostic, so keep it stable.
type RuntimeIssueCode string

const (
	// RuntimeIssueDataMountMissing is raised when no /data mount is present.
	RuntimeIssueDataMountMissing RuntimeIssueCode = "data_mount_missing"
	// RuntimeIssueDataMountReadOnly is raised when /data is mounted read-only.
	RuntimeIssueDataMountReadOnly RuntimeIssueCode = "data_mount_read_only"
	// RuntimeIssueRootfsReadOnlyNsswitch is raised when the root filesystem is
	// read-only but the image was not told to skip its /etc/nsswitch.conf
	// rewrite. The entrypoint fails and the container restart-loops.
	RuntimeIssueRootfsReadOnlyNsswitch RuntimeIssueCode = "read_only_root_blocks_nsswitch"
	// RuntimeIssueBootstrapCapabilitiesMissing is raised when the container
	// starts as root and drops privileges but no longer holds the capabilities
	// that the drop needs.
	RuntimeIssueBootstrapCapabilitiesMissing RuntimeIssueCode = "bootstrap_capabilities_missing"
	// RuntimeIssueNoNewPrivilegesDisabled is raised when no-new-privileges was
	// replaced instead of extended by an override.
	RuntimeIssueNoNewPrivilegesDisabled RuntimeIssueCode = "no_new_privileges_disabled"
	// RuntimeIssuePrivilegedMode is raised for full privilege mode.
	RuntimeIssuePrivilegedMode RuntimeIssueCode = "privileged_mode"
	// RuntimeIssueWritableRootfs is raised when the read-only root default was
	// turned off.
	RuntimeIssueWritableRootfs RuntimeIssueCode = "writable_rootfs"
	// RuntimeIssueNonNumericUser is raised when config.User names a user that
	// Docker can only resolve through the image's /etc/passwd.
	RuntimeIssueNonNumericUser RuntimeIssueCode = "non_numeric_user"
	// RuntimeIssueCustomEntrypoint is raised when an override replaces the
	// image entrypoint that performs first-boot preparation.
	RuntimeIssueCustomEntrypoint RuntimeIssueCode = "custom_entrypoint"
	// RuntimeIssueTmpfsMissing is raised when /tmp is not a tmpfs on a
	// read-only root.
	RuntimeIssueTmpfsMissing RuntimeIssueCode = "tmpfs_missing"
	// RuntimeIssueTmpfsTooSmall is raised when /tmp is too small for unpacking
	// a server jar or mod archive.
	RuntimeIssueTmpfsTooSmall RuntimeIssueCode = "tmpfs_too_small"
	// RuntimeIssueTmpfsExec is raised when /tmp stays executable.
	RuntimeIssueTmpfsExec RuntimeIssueCode = "tmpfs_exec"
	// RuntimeIssueHostNetwork is raised for host networking.
	RuntimeIssueHostNetwork RuntimeIssueCode = "host_network"
	// RuntimeIssueHostDevice is raised for a host device mapping.
	RuntimeIssueHostDevice RuntimeIssueCode = "host_device"
	// RuntimeIssueDataOwnerMismatch is raised when the container runs as a
	// numeric user that cannot own the world directory the panel created.
	RuntimeIssueDataOwnerMismatch RuntimeIssueCode = "data_owner_mismatch"
)

// RuntimeIssue is one compatibility finding with an actionable remedy.
type RuntimeIssue struct {
	Code     RuntimeIssueCode
	Severity RuntimeIssueSeverity
	// Detail says what the effective configuration is.
	Detail string
	// Remedy says what to change. It never suggests weakening confinement
	// beyond what the image actually needs.
	Remedy string
}

// Error renders the issue as a single diagnostic sentence.
func (i RuntimeIssue) Error() string {
	return fmt.Sprintf("%s [%s]: %s; %s", i.Code, i.Severity, i.Detail, i.Remedy)
}

// RuntimeSpec is the fully-resolved configuration that is about to be sent to
// the Docker daemon, plus the server it belongs to. Because it is taken after
// the defaults and every operator override have been applied, a check here
// sees exactly what the daemon will run rather than what the helpers meant.
type RuntimeSpec struct {
	// Image is the resolved image reference.
	Image string
	// DataPath is the host directory bound to /data.
	DataPath string
	// MinecraftImage marks an image that follows the itzg first-boot
	// contract: start as root, prepare /data, rewrite /etc/nsswitch.conf, then
	// drop to the unprivileged game user through gosu.
	MinecraftImage bool
	// DataOwnerUID is the owner of DataPath when the platform can report it.
	// It lets the profile tell a supported non-root path (the directory was
	// prepared for that uid) from one that would fail at boot.
	DataOwnerUID *int
	// Overrides are the operator overrides that produced this configuration.
	Overrides *v1.DockerOverrides
	// Config and HostConfig are the resolved daemon request.
	Config     *container.Config
	HostConfig *container.HostConfig
}

// RuntimeProfile is the effective, inspectable runtime description of one
// container: what it runs as, what it can write, and how confined it is.
type RuntimeProfile struct {
	Image             string
	MinecraftImage    bool
	User              string
	RootStartDropUser bool
	ReadOnlyRootfs    bool
	DataMount         string
	DataWritable      bool
	Tmpfs             map[string]string
	CapAdd            []string
	CapDrop           []string
	Privileged        bool
	NoNewPrivileges   bool
	NetworkMode       string
	Devices           []string
	Entrypoint        []string
	Issues            []RuntimeIssue
}

// Fatal reports whether any issue blocks a boot.
func (p RuntimeProfile) Fatal() bool {
	for _, issue := range p.Issues {
		if issue.Severity == RuntimeFatal {
			return true
		}
	}
	return false
}

// Description renders a one-line summary of the effective profile for logs and
// diagnostics.
func (p RuntimeProfile) Description() string {
	user := p.User
	if user == "" {
		user = "image default"
	}
	rootfs := "read-only"
	if !p.ReadOnlyRootfs {
		rootfs = "writable"
	}
	data := "missing"
	if p.DataMount != "" {
		data = p.DataMount
		if p.DataWritable {
			data += " (rw)"
		} else {
			data += " (ro)"
		}
	}
	return fmt.Sprintf("image=%s user=%s rootfs=%s data=%s nnp=%t privileged=%t net=%s",
		p.Image, user, rootfs, data, p.NoNewPrivileges, p.Privileged, p.NetworkMode)
}

// DescribeRuntime returns the effective profile of a resolved runtime spec
// together with its compatibility issues.
func DescribeRuntime(spec RuntimeSpec) RuntimeProfile {
	profile := RuntimeProfile{
		Image:             spec.Image,
		MinecraftImage:    spec.MinecraftImage,
		RootStartDropUser: spec.MinecraftImage,
		Tmpfs:             map[string]string{},
	}
	if spec.HostConfig != nil {
		profile.CapAdd = append([]string(nil), spec.HostConfig.CapAdd...)
		profile.CapDrop = append([]string(nil), spec.HostConfig.CapDrop...)
		profile.Privileged = spec.HostConfig.Privileged
		profile.NoNewPrivileges = hasNoNewPrivileges(spec.HostConfig.SecurityOpt)
		profile.NetworkMode = string(spec.HostConfig.NetworkMode)
		profile.ReadOnlyRootfs = spec.HostConfig.ReadonlyRootfs
		for _, m := range spec.HostConfig.Mounts {
			if m.Target == "/data" {
				profile.DataMount = m.Source
				profile.DataWritable = !m.ReadOnly
			}
		}
		for target, opts := range spec.HostConfig.Tmpfs {
			profile.Tmpfs[target] = opts
		}
		for _, d := range spec.HostConfig.Devices {
			profile.Devices = append(profile.Devices, d.PathOnHost+":"+d.PathInContainer)
		}
	}
	if spec.Config != nil {
		profile.User = strings.TrimSpace(spec.Config.User)
		profile.Entrypoint = append([]string(nil), spec.Config.Entrypoint...)
	}
	if uid, ok := resolveRuntimeUser(profile.User); ok {
		// An explicit non-root user cannot perform the root-then-drop
		// bootstrap; the image must already own the data.
		profile.RootStartDropUser = spec.MinecraftImage && uid == 0
	}
	profile.Issues = runtimeIssues(spec, profile)
	sort.SliceStable(profile.Issues, func(a, b int) bool {
		if profile.Issues[a].Severity != profile.Issues[b].Severity {
			return profile.Issues[a].Severity == RuntimeFatal
		}
		return profile.Issues[a].Code < profile.Issues[b].Code
	})
	return profile
}

// PreflightRuntime inspects the resolved configuration and returns an error
// when it cannot boot the configured image. The returned error wraps
// ErrRuntimeIncompatible and every fatal issue.
func PreflightRuntime(spec RuntimeSpec) error {
	return PreflightProfile(DescribeRuntime(spec))
}

// PreflightProfile is PreflightRuntime for an already-described profile.
func PreflightProfile(profile RuntimeProfile) error {
	var fatal []RuntimeIssue
	for _, issue := range profile.Issues {
		if issue.Severity == RuntimeFatal {
			fatal = append(fatal, issue)
		}
	}
	if len(fatal) == 0 {
		return nil
	}
	parts := make([]string, 0, len(fatal))
	for _, issue := range fatal {
		parts = append(parts, issue.Error())
	}
	return fmt.Errorf("%w: %s (%s)", ErrRuntimeIncompatible, strings.Join(parts, "; "), profile.Description())
}

// runtimeIssues applies the compatibility rules to a resolved profile.
func runtimeIssues(spec RuntimeSpec, profile RuntimeProfile) []RuntimeIssue {
	var issues []RuntimeIssue

	if profile.DataMount == "" {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueDataMountMissing,
			Severity: RuntimeFatal,
			Detail:   "no volume is mounted at /data",
			Remedy:   "mount the server data directory at /data; the image writes the world there",
		})
	} else if !profile.DataWritable {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueDataMountReadOnly,
			Severity: RuntimeFatal,
			Detail:   fmt.Sprintf("/data is mounted read-only from %q", profile.DataMount),
			Remedy:   "make the /data mount writable; a read-only world cannot be created or updated",
		})
	}

	if uid, ok := resolveRuntimeUser(profile.User); ok && uid != 0 && spec.DataOwnerUID != nil && *spec.DataOwnerUID != uid {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueDataOwnerMismatch,
			Severity: RuntimeFatal,
			Detail:   fmt.Sprintf("the container runs as uid %d but the world directory is owned by uid %d", uid, *spec.DataOwnerUID),
			Remedy:   "chown the data directory to the runtime uid:gid, or run the image as root and let its bootstrap drop privileges",
		})
	}

	if profile.MinecraftImage && profile.ReadOnlyRootfs && !hasEnv(spec.Config, skipNsswitchConfEnv, "TRUE") {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueRootfsReadOnlyNsswitch,
			Severity: RuntimeFatal,
			Detail:   "the root filesystem is read-only but the image may still rewrite /etc/nsswitch.conf during first boot",
			Remedy:   "set SKIP_NSSWITCH_CONF=TRUE, or set CARBON_WRITABLE_ROOTFS=1 when the image must write outside /data (both require the elevated Docker permission)",
		})
	}

	if profile.MinecraftImage && profile.RootStartDropUser && hasAllCapDrop(profile.CapDrop) {
		if missing := missingCapabilities(profile.CapAdd, minecraftBootstrapCapabilities()); len(missing) > 0 {
			issues = append(issues, RuntimeIssue{
				Code:     RuntimeIssueBootstrapCapabilitiesMissing,
				Severity: RuntimeFatal,
				Detail:   fmt.Sprintf("the container starts as root and drops privileges but CapDrop=ALL removed %s", strings.Join(missing, ", ")),
				Remedy:   "keep CHOWN, SETGID and SETUID in cap_add; the entrypoint needs them to prepare /data and drop to the game user",
			})
		}
	}

	if !profile.NoNewPrivileges && !profile.Privileged {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueNoNewPrivilegesDisabled,
			Severity: RuntimeWarning,
			Detail:   "no-new-privileges is not set on the container",
			Remedy:   "add no-new-privileges:true to security_opt; an override replaced the default instead of extending it",
		})
	}

	if profile.Privileged {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssuePrivilegedMode,
			Severity: RuntimeWarning,
			Detail:   "the container runs in privileged mode",
			Remedy:   "remove privileged and add only the specific capability or device the image needs",
		})
	}

	if !profile.ReadOnlyRootfs {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueWritableRootfs,
			Severity: RuntimeWarning,
			Detail:   "the container root filesystem is writable",
			Remedy:   "unset CARBON_WRITABLE_ROOTFS unless the image writes outside /data",
		})
	}

	if profile.User != "" && !isNumericUser(profile.User) {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueNonNumericUser,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("user %q is resolved through the image's /etc/passwd", profile.User),
			Remedy:   "use a numeric uid:gid so the container starts even when the image lacks that account",
		})
	}

	if len(profile.Entrypoint) > 0 && profile.RootStartDropUser {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueCustomEntrypoint,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("the image entrypoint is replaced by %q", strings.Join(profile.Entrypoint, " ")),
			Remedy:   "keep the image entrypoint, or reproduce its /data preparation and user drop in the replacement",
		})
	}

	if profile.ReadOnlyRootfs {
		opts, ok := profile.Tmpfs["/tmp"]
		switch {
		case !ok:
			issues = append(issues, RuntimeIssue{
				Code:     RuntimeIssueTmpfsMissing,
				Severity: RuntimeWarning,
				Detail:   "/tmp is not a tmpfs on a read-only root",
				Remedy:   "mount /tmp as a tmpfs so the image can unpack archives",
			})
		default:
			if size, ok := tmpfsSizeBytes(opts); ok && size < 16<<20 {
				issues = append(issues, RuntimeIssue{
					Code:     RuntimeIssueTmpfsTooSmall,
					Severity: RuntimeWarning,
					Detail:   fmt.Sprintf("/tmp tmpfs is %s", humanBytes(size)),
					Remedy:   "give /tmp at least 64m; a modpack or world archive can fill a small tmpfs and abort the boot",
				})
			}
			if tmpfsAllowsExec(opts) {
				issues = append(issues, RuntimeIssue{
					Code:     RuntimeIssueTmpfsExec,
					Severity: RuntimeWarning,
					Detail:   "/tmp allows executing files",
					Remedy:   "keep noexec on /tmp",
				})
			}
		}
	}

	if strings.EqualFold(profile.NetworkMode, "host") {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueHostNetwork,
			Severity: RuntimeFatal,
			Detail:   "the container shares the host network namespace",
			Remedy:   "use the default bridge or a dedicated network; host networking bypasses the panel's port and proxy model",
		})
	}

	if len(profile.Devices) > 0 {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueHostDevice,
			Severity: RuntimeFatal,
			Detail:   fmt.Sprintf("host devices are mapped into the container: %s", strings.Join(profile.Devices, ", ")),
			Remedy:   "remove the device mapping; no Minecraft server needs host device access",
		})
	}

	return issues
}

// IsMinecraftImage reports whether an image reference is the itzg Minecraft
// server image, which is the image that follows the root-start-then-drop
// bootstrap contract the profile checks.
func IsMinecraftImage(image string) bool {
	image = strings.ToLower(strings.TrimSpace(image))
	image = strings.TrimPrefix(image, "docker.io/")
	return strings.HasPrefix(image, "itzg/minecraft-server")
}

func hasNoNewPrivileges(opts []string) bool {
	for _, opt := range opts {
		trimmed := strings.TrimSpace(opt)
		if strings.EqualFold(trimmed, "no-new-privileges:true") || strings.EqualFold(trimmed, "no-new-privileges=true") {
			return true
		}
	}
	return false
}

func hasAllCapDrop(drop []string) bool {
	for _, c := range drop {
		if strings.EqualFold(strings.TrimSpace(c), "ALL") {
			return true
		}
	}
	return false
}

// missingCapabilities returns the required capabilities that are absent from
// add, case-insensitively and with an optional CAP_ prefix.
func missingCapabilities(add, required []string) []string {
	present := make(map[string]struct{}, len(add))
	for _, c := range add {
		present[strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(c)), "CAP_")] = struct{}{}
	}
	var missing []string
	for _, c := range required {
		if _, ok := present[strings.TrimPrefix(strings.ToUpper(c), "CAP_")]; !ok {
			missing = append(missing, c)
		}
	}
	return missing
}

// resolveRuntimeUser parses the numeric component of a Docker user string.
// "1000", "1000:1000" and "0:0" resolve; a name does not.
func resolveRuntimeUser(user string) (int, bool) {
	user = strings.TrimSpace(user)
	if user == "" {
		return 0, false
	}
	if idx := strings.IndexByte(user, ':'); idx >= 0 {
		user = user[:idx]
	}
	uid, err := strconv.Atoi(user)
	if err != nil {
		return 0, false
	}
	return uid, true
}

func isNumericUser(user string) bool {
	_, ok := resolveRuntimeUser(user)
	return ok
}

func hasEnv(config *container.Config, key, want string) bool {
	if config == nil {
		return false
	}
	for _, env := range config.Env {
		k, v, ok := strings.Cut(env, "=")
		if !ok {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), key) && strings.EqualFold(strings.TrimSpace(v), want) {
			return true
		}
	}
	return false
}

// tmpfsSizeBytes parses the size= option of a tmpfs mount string.
func tmpfsSizeBytes(opts string) (int64, bool) {
	for _, part := range strings.Split(opts, ",") {
		part = strings.TrimSpace(part)
		key, value, ok := strings.Cut(part, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "size") {
			continue
		}
		return parseDockerSize(strings.TrimSpace(value))
	}
	return 0, false
}

// parseDockerSize accepts the size suffixes Docker accepts.
func parseDockerSize(value string) (int64, bool) {
	if value == "" {
		return 0, false
	}
	multiplier := int64(1)
	switch value[len(value)-1] {
	case 'k', 'K':
		multiplier = 1 << 10
	case 'm', 'M':
		multiplier = 1 << 20
	case 'g', 'G':
		multiplier = 1 << 30
	}
	number := value
	if multiplier != 1 {
		number = value[:len(value)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(number), 10, 64)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n * multiplier, true
}

func tmpfsAllowsExec(opts string) bool {
	for _, part := range strings.Split(opts, ",") {
		if strings.EqualFold(strings.TrimSpace(part), "exec") {
			return true
		}
	}
	return false
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%dG", n>>30)
	case n >= 1<<20:
		return fmt.Sprintf("%dM", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%dK", n>>10)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// runtimeSpecFor assembles the qualification input for a resolved container
// request, including the world directory's owner when the platform reports it.
func runtimeSpecFor(image, dataPath string, server *db.Server, config *container.Config, hostConfig *container.HostConfig) RuntimeSpec {
	spec := RuntimeSpec{
		Image:          image,
		DataPath:       dataPath,
		MinecraftImage: IsMinecraftImage(image),
		Config:         config,
		HostConfig:     hostConfig,
	}
	if server != nil {
		spec.Overrides = server.DockerOverrides
	}
	if uid, ok := dataOwnerUID(dataPath); ok {
		spec.DataOwnerUID = &uid
	}
	return spec
}
