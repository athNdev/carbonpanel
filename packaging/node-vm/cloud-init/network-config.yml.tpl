# Reproducible NoCloud network-config template. No credentials, no static
# controller addresses baked in; DHCP by default. Static addressing, if needed,
# is supplied per-VM at provisioning time as public metadata and is never part
# of the shared image or its seed.
version: 2
ethernets:
  uplinks:
    match:
      name: "en*"
    dhcp4: true
    dhcp6: false
