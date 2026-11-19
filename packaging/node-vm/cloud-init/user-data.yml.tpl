#cloud-config
# Reproducible node-VM cloud-init user-data template.
# NO secret, token, private key or enrollment input is present in this file,
# the generated seed, or the QCOW2 image. First boot generates all key material
# locally (see cloud-init/firstboot/enroll.py) and exports only a digest-only
# bundle for out-of-band operator registration under the SHARED ADR0019 contract.
# Runtime values (instance-id, node label, controller URL) are inserted by the
# build driver; they are public metadata, not credentials.

hostname: cp-node-@@NODE_LABEL@@
fqdn: cp-node-@@NODE_LABEL@@.local

write_files:
  - path: /etc/carbon-node/firstboot/enroll.py
    owner: root:root
    permissions: '0700'
    encoding: b64
    content: @@FIRSTBOOT_SCRIPT_B64@@
  - path: /etc/carbon-node/firstboot/policy.json
    owner: root:root
    permissions: '0600'
    content: |
      @@ENROLLMENT_POLICY_JSON@@

runcmd:
  - [python3, /etc/carbon-node/firstboot/enroll.py, first-boot, --policy, /etc/carbon-node/firstboot/policy.json, --instance-id, @@INSTANCE_ID@@, --node-label, @@NODE_LABEL@@]

power_state:
  mode: reboot
  condition: true
