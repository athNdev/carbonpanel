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
	// RuntimeIssueMemoryLimitUnset is raised when the container has no memory
	// limit, so one server can consume the node.
	RuntimeIssueMemoryLimitUnset RuntimeIssueCode = "memory_limit_unset"
	// RuntimeIssueSwapEnabled is raised when the 0-swap policy was overridden.
	RuntimeIssueSwapEnabled RuntimeIssueCode = "swap_enabled"
	// RuntimeIssuePidsLimitUnset is raised when the fork-bomb guard is absent.
	RuntimeIssuePidsLimitUnset RuntimeIssueCode = "pids_limit_unset"
	// RuntimeIssueWritableLayerUnbounded is raised for a writable root with no
	// writable-layer cap.
	RuntimeIssueWritableLayerUnbounded RuntimeIssueCode = "writable_layer_unbounded"
	// RuntimeIssueStorageQuotaIsNotAWorldQuota is raised when a storage-opt
	// size is presented as if it bounded the world.
	RuntimeIssueStorageQuotaIsNotAWorldQuota RuntimeIssueCode = "storage_quota_is_not_a_world_quota"
	// RuntimeIssueWorldQuotaExceeded is raised when storage.world_quota_mb is
	// set and measured world bytes meet or exceed it. Creation is refused.
	RuntimeIssueWorldQuotaExceeded RuntimeIssueCode = "world_quota_exceeded"
	// RuntimeIssueWorldQuotaUnmeasured is raised when a world quota is set but
	// world bytes could not be measured, so admission fails closed.
	RuntimeIssueWorldQuotaUnmeasured RuntimeIssueCode = "world_quota_unmeasured"
	// RuntimeIssueWritableLayerCapIgnored is raised when a writable-layer cap
	// is configured but the root is read-only, so nothing is capped.
	RuntimeIssueWritableLayerCapIgnored RuntimeIssueCode = "writable_layer_cap_ignored"
	// RuntimeIssueMemoryLimitUnsupported is raised when the container requests a
	// memory cap but the daemon reports it cannot enforce one.
	RuntimeIssueMemoryLimitUnsupported RuntimeIssueCode = "memory_limit_unsupported"
	// RuntimeIssueSwapLimitUnsupported is raised when swap is enabled on the
	// request but the daemon reports swap limits are unavailable.
	RuntimeIssueSwapLimitUnsupported RuntimeIssueCode = "swap_limit_unsupported"
	// RuntimeIssuePidsLimitUnsupported is raised when a process cap is set but
	// the daemon reports pids limits are unavailable.
	RuntimeIssuePidsLimitUnsupported RuntimeIssueCode = "pids_limit_unsupported"
	// RuntimeIssueRootlessCgroupUnqualified is raised when a rootless daemon is
	// not on cgroup v2, so cgroup resource flags may be ignored.
	RuntimeIssueRootlessCgroupUnqualified RuntimeIssueCode = "rootless_cgroup_unqualified"
	// RuntimeIssueWritableLayerCapUnsupported is raised when a writable-layer
	// cap is requested on a storage stack that does not support storage-opt size.
	RuntimeIssueWritableLayerCapUnsupported RuntimeIssueCode = "writable_layer_cap_unsupported"
	// RuntimeIssueImageUnpinned is raised when a Minecraft image is a moving
	// tag. It is a warning: the container can still boot, but a rebuilt tag
	// can change what the next recreate runs.
	RuntimeIssueImageUnpinned RuntimeIssueCode = "image_unpinned"
	// RuntimeIssueRootlessHostRootData is raised when a rootless daemon must run
	// the root-then-drop bootstrap but the world directory is still owned by
	// host uid 0. The entrypoint cannot chown it into the user namespace.
	RuntimeIssueRootlessHostRootData RuntimeIssueCode = "rootless_host_root_data"
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
	// RequestedMemoryMB is the memory the server record asked for, which the
	// effective container limit is derived from.
	RequestedMemoryMB int64
	// WritableLayerMB is the operator's writable-layer cap. It is applied only
	// when the root filesystem is writable.
	WritableLayerMB int64
	// WorldQuotaMB is panel admission against measured world bytes. 0 disables.
	// It is not a daemon, xfs, or volume quota and does not set WorldDiskEnforced.
	WorldQuotaMB int64
	// WorldBytes is the metrics collector's world-directory total when known.
	WorldBytes int64
	// WorldBytesKnown is false when a set quota could not be measured.
	WorldBytesKnown bool
	// DataOwnerUID is the owner of DataPath when the platform can report it.
	// It lets the profile tell a supported non-root path (the directory was
	// prepared for that uid) from one that would fail at boot.
	DataOwnerUID *int
	// Overrides are the operator overrides that produced this configuration.
	Overrides *v1.DockerOverrides
	// Daemon is the Docker daemon's reported limit capabilities when known.
	// Missing means qualification was not performed; no daemon issue is raised.
	Daemon *DaemonEnforcement
	// Config and HostConfig are the resolved daemon request.
	Config     *container.Config
	HostConfig *container.HostConfig
}

// ContainerLimits is the enforcement that is actually requested from the
// daemon, next to the limit the caller asked for. A zero effective value means
// no limit: it is never reported as if the requested limit applied.
type ContainerLimits struct {
	// RequestedMemoryMB is the server's configured memory.
	RequestedMemoryMB int64
	// RequestedMemoryBytes and RequestedMemorySwapBytes are HostConfig values
	// sent to Docker. MemoryBytes and MemorySwapBytes are effective: zero when
	// /info says the daemon cannot apply them.
	RequestedMemoryBytes     int64
	RequestedMemorySwapBytes int64
	MemoryBytes              int64
	MemorySwapBytes          int64
	// SwapDisabled is true when the request sets MemorySwap equal to Memory or
	// leaves both unset. It describes the request, not daemon capability.
	SwapDisabled bool
	// RequestedPidsLimit is the process cap on the request. PidsLimit is
	// effective, or 0 when the daemon default applies or /info says pids
	// limits are unsupported.
	RequestedPidsLimit int64
	PidsLimit          int64
	// CPULimit is the configured CPU cap, or 0 for unlimited.
	CPULimit float64
	// RequestedWritableLayerMB is storage-opt size on the request.
	// WritableLayerMB is effective, or 0 when the layer is unbounded, the
	// root is read-only, or /info says storage-opt size is unsupported.
	RequestedWritableLayerMB int64
	WritableLayerMB          int64
	// WorldDiskEnforced is true only when the daemon quotas the world bind.
	// Panel admission (storage.world_quota_mb) can refuse create without
	// setting this. storage-opt size never sets it, including overlay2+xfs.
	WorldDiskEnforced bool
}

// RuntimeProfile is the effective, inspectable runtime description of one
// container: what it runs as, what it can write, how confined it is, and which
// limits are actually enforced.
type RuntimeProfile struct {
	Image string
	// ImageDigest is the sha256 digest when the image reference is pinned.
	// Empty means the reference is a tag.
	ImageDigest       string
	ImagePinned       bool
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
	Limits            ContainerLimits
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
	return fmt.Sprintf("image=%s user=%s rootfs=%s data=%s nnp=%t privileged=%t net=%s memory=%dB pids=%d layer=%dMB",
		p.Image, user, rootfs, data, p.NoNewPrivileges, p.Privileged, p.NetworkMode,
		p.Limits.MemoryBytes, p.Limits.PidsLimit, p.Limits.WritableLayerMB)
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
	if digest, pinned := imageDigest(spec.Image); pinned {
		profile.ImageDigest = digest
		profile.ImagePinned = true
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
		profile.Limits = describeLimits(spec)
	}
	if spec.HostConfig == nil {
		profile.Limits = describeLimits(spec)
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

// describeLimits reports what the request actually enforces rather than what
// was asked for. A limit that /info says the daemon will not apply is
// reported as zero on the effective fields; the HostConfig request is kept on
// the Requested* fields so an ignored limit is never presented as enforced.
func describeLimits(spec RuntimeSpec) ContainerLimits {
	limits := ContainerLimits{RequestedMemoryMB: spec.RequestedMemoryMB}
	if spec.HostConfig == nil {
		return limits
	}
	res := spec.HostConfig.Resources
	limits.RequestedMemoryBytes = res.Memory
	limits.RequestedMemorySwapBytes = res.MemorySwap
	limits.MemoryBytes = res.Memory
	limits.MemorySwapBytes = res.MemorySwap
	limits.SwapDisabled = res.Memory == 0 || res.MemorySwap == res.Memory
	if res.PidsLimit != nil {
		limits.RequestedPidsLimit = *res.PidsLimit
		limits.PidsLimit = *res.PidsLimit
	}
	limits.CPULimit = float64(res.NanoCPUs) / 1e9
	if size, ok := spec.HostConfig.StorageOpt["size"]; ok {
		if bytes, parsed := parseDockerSize(size); parsed {
			limits.RequestedWritableLayerMB = bytes >> 20
			limits.WritableLayerMB = bytes >> 20
			// storage-opt sizes the container writable layer only. A bind mount
			// such as /data is outside it and keeps growing with the world.
			limits.WorldDiskEnforced = false
		}
	}
	applyDaemonEffectiveLimits(spec.Daemon, &limits)
	return limits
}

// applyDaemonEffectiveLimits zeros effective memory, swap, pids, and
// writable-layer values when Docker /info says those caps cannot apply.
// Requested* fields are left as the HostConfig the panel sent.
func applyDaemonEffectiveLimits(daemon *DaemonEnforcement, limits *ContainerLimits) {
	if daemon == nil {
		return
	}
	cgroupIgnored := daemon.Rootless && daemon.CgroupVersion != "2"
	if limits.MemoryBytes > 0 && (!daemon.MemoryLimitSupported || cgroupIgnored) {
		limits.MemoryBytes = 0
		limits.MemorySwapBytes = 0
	}
	if limits.PidsLimit > 0 && (!daemon.PidsLimitSupported || cgroupIgnored) {
		limits.PidsLimit = 0
	}
	if limits.WritableLayerMB > 0 && !daemon.WritableLayerQuotaSupported {
		limits.WritableLayerMB = 0
	}
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

	if spec.Daemon != nil && spec.Daemon.Rootless && profile.MinecraftImage && profile.RootStartDropUser &&
		spec.DataOwnerUID != nil && *spec.DataOwnerUID == 0 {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueRootlessHostRootData,
			Severity: RuntimeFatal,
			Detail:   "the data directory is owned by host uid 0 but the daemon runs rootless",
			Remedy:   "chown the data directory to the game uid (for example 1000) or the subuid your rootless daemon maps, then recreate; do not enable privileged mode or a writable root to compensate",
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
					Remedy:   "give /tmp at least 64m for small image scratch. Modpack downloads and native libraries use the data-volume work directory, not a larger tmpfs",
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

	if profile.MinecraftImage && !profile.ImagePinned {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueImageUnpinned,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("image %q is not pinned to a digest", profile.Image),
			Remedy:   "record the image as name@sha256:<digest> so a rebuilt tag cannot change the server; do not broaden privileges to compensate",
		})
	}

	issues = append(issues, limitIssues(profile)...)
	issues = append(issues, daemonLimitIssues(spec, profile)...)
	issues = append(issues, worldQuotaIssues(spec)...)

	if spec.WritableLayerMB > 0 && profile.ReadOnlyRootfs {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueWritableLayerCapIgnored,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("storage.container_writable_mb is %d MB but the root filesystem is read-only", spec.WritableLayerMB),
			Remedy:   "leave the cap at 0 for a read-only root, or set it only for an image that writes outside /data",
		})
	}

	return issues
}

// limitIssues reports a limit that is advertised but not applied. These are
// warnings: an unlimited container still boots, but one server can then starve
// the node, and an operator must be told the number they set is not in effect.
func limitIssues(profile RuntimeProfile) []RuntimeIssue {
	var issues []RuntimeIssue

	if profile.Limits.RequestedMemoryBytes == 0 {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueMemoryLimitUnset,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("no container memory limit is set (requested %d MB)", profile.Limits.RequestedMemoryMB),
			Remedy:   "set a server memory value; without a limit one server can exhaust the node",
		})
	}

	if !profile.Limits.SwapDisabled {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueSwapEnabled,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("swap is enabled (memory %d bytes, memory+swap %d bytes)", profile.Limits.RequestedMemoryBytes, profile.Limits.RequestedMemorySwapBytes),
			Remedy:   "set the swap limit equal to the memory limit; swapping game-server heap causes tick stalls",
		})
	}

	if profile.Limits.RequestedPidsLimit == 0 {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssuePidsLimitUnset,
			Severity: RuntimeWarning,
			Detail:   "no process limit is set",
			Remedy:   "set a pids limit so a fork bomb in one container cannot exhaust the node",
		})
	}

	if !profile.ReadOnlyRootfs && profile.Limits.RequestedWritableLayerMB == 0 {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueWritableLayerUnbounded,
			Severity: RuntimeWarning,
			Detail:   "the root filesystem is writable and the writable layer has no size cap",
			Remedy:   "set storage.container_writable_mb, or return the image to a read-only root",
		})
	}

	if profile.Limits.RequestedWritableLayerMB > 0 && profile.DataMount != "" {
		layerMB := profile.Limits.RequestedWritableLayerMB
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueStorageQuotaIsNotAWorldQuota,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("the %d MB writable-layer cap does not bound the world at %s", layerMB, profile.DataMount),
			Remedy:   "set storage.world_quota_mb to refuse create when measured world bytes meet the cap; storage-opt size does not quota a bind-mounted world",
		})
	}

	return issues
}

// worldQuotaIssues refuses create when an operator world quota is set and the
// measured world (metrics collector world bytes) meets or exceeds it. Unset
// (0) never refuses. A set quota that cannot be measured fails closed.
func worldQuotaIssues(spec RuntimeSpec) []RuntimeIssue {
	if spec.WorldQuotaMB <= 0 {
		return nil
	}
	if !spec.WorldBytesKnown {
		return []RuntimeIssue{{
			Code:     RuntimeIssueWorldQuotaUnmeasured,
			Severity: RuntimeFatal,
			Detail:   fmt.Sprintf("storage.world_quota_mb is %d MB but world bytes could not be measured", spec.WorldQuotaMB),
			Remedy:   "make the world directory readable to the panel, or unset storage.world_quota_mb",
		}}
	}
	quotaBytes := spec.WorldQuotaMB << 20
	if spec.WorldBytes < quotaBytes {
		return nil
	}
	return []RuntimeIssue{{
		Code:     RuntimeIssueWorldQuotaExceeded,
		Severity: RuntimeFatal,
		Detail:   fmt.Sprintf("measured world bytes %d meet or exceed storage.world_quota_mb %d MB", spec.WorldBytes, spec.WorldQuotaMB),
		Remedy:   "free world data or raise storage.world_quota_mb; this is panel admission, not a daemon or xfs quota",
	}}
}

// daemonLimitIssues compares the resolved request to daemon capabilities so an
// ignored limit is reported explicitly. Warnings only: the container may still
// boot, but the operator must not treat the number as enforced.
func daemonLimitIssues(spec RuntimeSpec, profile RuntimeProfile) []RuntimeIssue {
	if spec.Daemon == nil {
		return nil
	}
	daemon := spec.Daemon
	var issues []RuntimeIssue

	if daemon.Rootless && daemon.CgroupVersion != "2" {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueRootlessCgroupUnqualified,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("rootless Docker is on cgroup %q, not cgroup v2", daemon.CgroupVersion),
			Remedy:   "move the rootless daemon to cgroup v2 with delegation, or treat memory/swap/pids limits as advisory",
		})
	}

	if profile.Limits.RequestedMemoryBytes > 0 && !daemon.MemoryLimitSupported {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueMemoryLimitUnsupported,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("the container requests %d bytes of memory but the daemon reports memory limits are unsupported", profile.Limits.RequestedMemoryBytes),
			Remedy:   "use a daemon with cgroup support, or reduce load; the request may run without the cap",
		})
	}

	if profile.Limits.RequestedMemoryBytes > 0 && !daemon.SwapLimitSupported {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueSwapLimitUnsupported,
			Severity: RuntimeWarning,
			Detail:   "the container requests a swap policy but the daemon reports swap limits are unsupported",
			Remedy:   "use a daemon with swap cgroup support; MemorySwap may be ignored, including a 0-swap request",
		})
	}

	if profile.Limits.RequestedPidsLimit > 0 && !daemon.PidsLimitSupported {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssuePidsLimitUnsupported,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("the container requests pids limit %d but the daemon reports pids limits are unsupported", profile.Limits.RequestedPidsLimit),
			Remedy:   "use a daemon with pids cgroup support or accept the default process limit",
		})
	}

	if spec.WritableLayerMB > 0 && !profile.ReadOnlyRootfs && !daemon.WritableLayerQuotaSupported {
		issues = append(issues, RuntimeIssue{
			Code:     RuntimeIssueWritableLayerCapUnsupported,
			Severity: RuntimeWarning,
			Detail:   fmt.Sprintf("storage.container_writable_mb is %d MB but the %s storage driver cannot enforce storage-opt size", spec.WritableLayerMB, daemon.StorageDriver),
			Remedy:   "use overlay2 on xfs with pquota for a layer cap, or leave the writable layer unbounded and rely on node free space",
		})
	}

	return issues
}

// imageDigest reports the sha256 digest of a pinned reference (name@sha256:<hex>).
// A tag, including :latest, is not pinned.
func imageDigest(image string) (string, bool) {
	image = strings.TrimSpace(image)
	at := strings.LastIndex(image, "@")
	if at < 0 || at == len(image)-1 {
		return "", false
	}
	digest := image[at+1:]
	if !strings.HasPrefix(strings.ToLower(digest), "sha256:") || len(digest) < len("sha256:")+64 {
		return "", false
	}
	return digest, true
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
