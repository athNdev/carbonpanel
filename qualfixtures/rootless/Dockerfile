# SPDX-License-Identifier: MPL-2.0
# Disposable rootless-daemon qualification fixture.
#
# Base is the same clean Ubuntu 24.04 / systemd 255 / real dockerd 29.1.3
# fixture used for the shipped ROOTFUL unit boot. It already carries uidmap,
# slirp4netns, iptables and subuid/subgid for user `ubuntu`. This layer only
# adds the user-session bus that a real `systemd --user` manager needs.
#
# rootlesskit and dockerd-rootless.sh are bind-mounted from the owning Linux
# host at run time; they are daemon prerequisites, not unit substitutions.
FROM cp-node-systemd-qual:latest
ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update \
 && apt-get install -y --no-install-recommends dbus dbus-user-session libpam-systemd \
 && apt-get clean \
 && rm -rf /var/lib/apt/lists/*
STOPSIGNAL SIGRTMIN+3
CMD ["/lib/systemd/systemd"]
