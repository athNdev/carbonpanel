#!/usr/bin/env bash
# SPDX-License-Identifier: MPL-2.0
# Runs INSIDE the owned disposable chroot, never on a user host. Base-specific
# values come from the manifest via CT_* env; the Docker/controller seam is shared.
set -euo pipefail
export DEBIAN_FRONTEND=noninteractive
WORK=/work
SUITE="${CT_APT_SUITE:?missing suite}"
REPO="${CT_APT_REPO:?missing repo}"
KEY_FP="${CT_KEY_FINGERPRINT:?missing key fingerprint}"
# shellcheck disable=SC2086
PACKAGES="${CT_PACKAGES:?missing package list}"

cat > /usr/sbin/policy-rc.d <<'EOP'
#!/bin/sh
exit 101
EOP
chmod 0755 /usr/sbin/policy-rc.d
apt-get update
apt-get install -y --no-install-recommends ca-certificates gnupg \
  systemd-sysv ifupdown dhcpcd-base iproute2 openssh-server
install -d -m 0755 /etc/apt/keyrings
cp "$WORK/docker.gpg" /etc/apt/keyrings/docker.gpg
chmod a+r /etc/apt/keyrings/docker.gpg
printf 'deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.gpg] %s %s stable\n' \
  "$REPO" "$SUITE" > /etc/apt/sources.list.d/docker.list
# Signed trixie-security updates from the official Debian archive inside guest.
printf 'deb [signed-by=/etc/apt/trusted.gpg.d/debian-archive-key-13.gpg] https://deb.debian.org/debian-security %s-security main\n' \
  "$SUITE" > /etc/apt/sources.list.d/debian-security.list
apt-get update
# Record the exact signed base Release actually used, then keep upstream docs.
base_release=$(find /var/lib/apt/lists -maxdepth 1 -name '*_InRelease' | grep -i 'deb.debian.org.*trixie' | head -1)
[ -n "$base_release" ] || { echo "signed trixie InRelease not found"; exit 1; }
base_release_sha=$(sha256sum "$base_release" | cut -d' ' -f1)
mkdir -p "$WORK/debs"
cd "$WORK/debs"
# shellcheck disable=SC2086
apt-get download $PACKAGES
# Install the locally downloaded, signed-repo debs with apt so dependencies
# are resolved from the same signed repository (not dpkg's bare file install).
# shellcheck disable=SC2086
apt-get install -y --no-install-recommends $WORK/debs/*.deb
# Fresh guest identity: no pre-baked machine ID or SSH host keys.
rm -f /etc/machine-id /var/lib/dbus/machine-id /etc/ssh/ssh_host_*
for deb in "$WORK"/debs/*.deb; do
  name=$(dpkg-deb -f "$deb" Package)
  version=$(dpkg-deb -f "$deb" Version)
  installed=$(dpkg-query -W -f='${Version}' "$name")
  [ "$installed" = "$version" ] || { echo "installed $name $installed != signed $version"; exit 1; }
done
docker compose version >/dev/null
rm -f /usr/sbin/policy-rc.d
systemctl --root=/ enable docker.service containerd.service
[ -f /usr/share/doc/base-files/copyright ] || { echo "upstream copyright metadata missing"; exit 1; }
{
  printf '{\n'
  printf '  "schema": "carbonpanel.proxmox-controller-ct.docker-receipt/v1",\n'
  printf '  "signed_repo_required": true,\n'
  printf '  "key_fingerprint": "%s",\n' "$KEY_FP"
  printf '  "fixture": false,\n'
  printf '  "publishable": true,\n'
  printf '  "base_suite": "%s",\n' "$SUITE"
  printf '  "base_release_sha256": "%s",\n' "$base_release_sha"
  printf '  "packages": ['
  first=1
  for deb in "$WORK"/debs/*.deb; do
    name=$(dpkg-deb -f "$deb" Package)
    [ "$first" -eq 1 ] || printf ', '
    printf '"%s"' "$name"
    first=0
  done
  printf '],\n  "package_sha256": {\n'
  first=1
  for deb in "$WORK"/debs/*.deb; do
    name=$(dpkg-deb -f "$deb" Package)
    digest=$(sha256sum "$deb" | cut -d' ' -f1)
    [ "$first" -eq 1 ] || printf ',\n'
    printf '    "%s": "%s"' "$name" "$digest"
    first=0
  done
  printf '\n  },\n  "package_versions": {\n'
  first=1
  for deb in "$WORK"/debs/*.deb; do
    name=$(dpkg-deb -f "$deb" Package)
    version=$(dpkg-deb -f "$deb" Version)
    [ "$first" -eq 1 ] || printf ',\n'
    printf '    "%s": "%s"' "$name" "$version"
    first=0
  done
  printf '\n  }\n}\n'
} > "$WORK/package-receipt.json"
echo "signed package receipt written"
