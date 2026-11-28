# SPDX-License-Identifier: MPL-2.0
"""Deterministic, offline GPT expansion for a disposable node-VM boot disk.

``qemu-img create -b base.qcow2 overlay.qcow2 20G`` changes the overlay's
virtual size but not the guest partition table: the primary GPT header still
records the base image's ``alternate_lba`` and ``last_usable_lba``, and the
backup header stays at the base image's last LBA. Linux partition scanners then
see a table that describes a 3.5 GiB disk on a 20 GiB device. The retained arm64
qualification report records that mismatch ("original GPT alternate header at
the base image end") together with ``/dev/disk/by-label/BOOT`` and
``/dev/disk/by-label/UEFI`` timeouts and emergency mode before ``growpart``.

This module rewrites only the GPT metadata of a *copy* of the base image so the
primary and backup headers both describe the requested disk size. It never opens
the pinned base image, never touches a host block device, and performs a fixed,
bounded number of sector writes (the protective MBR, the primary header, the
backup entry array and the backup header). Existing partition entries are
preserved byte-for-byte; the guest still owns partition/filesystem growth.

The plan and apply steps are pure file I/O, so the regression tests run offline
with synthetic GPT images and no QEMU, Docker, guestfs tool or elevated access.
"""
import os
import re
import struct
import zlib
from pathlib import Path

SECTOR_SIZE = 512
GPT_SIGNATURE = b"EFI PART"
GPT_REVISION = 0x00010000
PROTECTIVE_MBR_TYPE = 0xEE
MIN_HEADER_SIZE = 92
MAX_HEADER_SIZE = SECTOR_SIZE
MIN_ENTRY_SIZE = 128
MAX_ENTRIES = 4096
MAX_ENTRY_ARRAY_BYTES = 1024 * 1024
# The protective MBR partition entry: type at byte 4, first LBA at 8, sectors at 12.
MBR_PARTITION_OFFSET = 446
MBR_MAX_SECTORS = 0xFFFFFFFF

_SIZE_RE = re.compile(r"^(?P<value>[0-9]+)(?P<suffix>[KMGT])?B?$", re.IGNORECASE)
_SIZE_FACTORS = {"": 1, "K": 1024, "M": 1024 ** 2, "G": 1024 ** 3, "T": 1024 ** 4}


class GptError(ValueError):
    """Raised when a disk cannot be safely identified or expanded."""


def parse_disk_size(value):
    """Parse a qemu-style size (``20G``, ``20480M``, ``21474836480``) to bytes.

    Suffixes are binary multiples, matching qemu-img. Bare numbers are bytes.
    """
    if isinstance(value, bool):
        raise GptError("disk size must be a positive integer byte count or a "
                       "qemu-style size such as 20G")
    if isinstance(value, int):
        if value <= 0:
            raise GptError("disk size must be positive")
        return value
    if not isinstance(value, str):
        raise GptError("disk size must be a string or integer")
    match = _SIZE_RE.fullmatch(value.strip())
    if not match:
        raise GptError("invalid disk size %r; use bytes or a size such as 20G" % value)
    number = int(match.group("value"))
    if number <= 0:
        raise GptError("disk size must be positive")
    return number * _SIZE_FACTORS[(match.group("suffix") or "").upper()]


def _u32(data, offset):
    return struct.unpack_from("<I", data, offset)[0]


def _u64(data, offset):
    return struct.unpack_from("<Q", data, offset)[0]


def _crc32(data):
    return zlib.crc32(data) & 0xFFFFFFFF


def _read_exact(handle, offset, length):
    handle.seek(offset)
    data = handle.read(length)
    if len(data) != length:
        raise GptError("short read at offset %d (wanted %d bytes)" % (offset, length))
    return data


def _set_header_crc(header, header_size):
    struct.pack_into("<I", header, 16, 0)
    struct.pack_into("<I", header, 16, _crc32(bytes(header[:header_size])))


def _read_header(handle, lba):
    raw = _read_exact(handle, lba * SECTOR_SIZE, SECTOR_SIZE)
    if raw[:8] != GPT_SIGNATURE:
        raise GptError("no GPT header signature at LBA %d" % lba)
    revision = _u32(raw, 8)
    if revision != GPT_REVISION:
        raise GptError("unsupported GPT revision 0x%08x at LBA %d" % (revision, lba))
    header_size = _u32(raw, 12)
    if header_size < MIN_HEADER_SIZE or header_size > MAX_HEADER_SIZE:
        raise GptError("invalid GPT header size %d at LBA %d" % (header_size, lba))
    stored_crc = _u32(raw, 16)
    check = bytearray(raw[:header_size])
    check[16:20] = b"\x00\x00\x00\x00"
    if _crc32(bytes(check)) != stored_crc:
        raise GptError("GPT header CRC mismatch at LBA %d" % lba)
    header = {
        "lba": lba,
        "raw": raw,
        "header_size": header_size,
        "my_lba": _u64(raw, 24),
        "alternate_lba": _u64(raw, 32),
        "first_usable_lba": _u64(raw, 40),
        "last_usable_lba": _u64(raw, 48),
        "partition_entry_lba": _u64(raw, 72),
        "num_entries": _u32(raw, 80),
        "entry_size": _u32(raw, 84),
        "entries_crc": _u32(raw, 88),
    }
    if header["my_lba"] != lba:
        raise GptError("GPT header at LBA %d claims my_lba %d"
                       % (lba, header["my_lba"]))
    if header["num_entries"] < 1 or header["num_entries"] > MAX_ENTRIES:
        raise GptError("invalid GPT entry count %d" % header["num_entries"])
    entry_size = header["entry_size"]
    if (entry_size < MIN_ENTRY_SIZE or entry_size % 8
            or entry_size & (entry_size - 1) or entry_size > MAX_ENTRY_ARRAY_BYTES):
        raise GptError("invalid GPT entry size %d" % entry_size)
    entries_bytes = header["num_entries"] * entry_size
    if entries_bytes > MAX_ENTRY_ARRAY_BYTES:
        raise GptError("GPT entry array is too large (%d bytes)" % entries_bytes)
    if header["partition_entry_lba"] < 2:
        raise GptError("GPT entry array overlaps the primary header")
    header["entries_bytes"] = entries_bytes
    return header


def _read_entries(handle, header):
    offset = header["partition_entry_lba"] * SECTOR_SIZE
    entries = _read_exact(handle, offset, header["entries_bytes"])
    if _crc32(entries) != header["entries_crc"]:
        raise GptError("GPT partition entry array CRC mismatch")
    return entries


def _partition_extents(entries, num_entries, entry_size):
    """Return used partition records as 1-based index, first and last LBA."""
    partitions = []
    for index in range(num_entries):
        base = index * entry_size
        entry = entries[base:base + entry_size]
        if len(entry) != entry_size:
            raise GptError("truncated GPT partition entry %d" % (index + 1))
        if entry[:16] == b"\x00" * 16:
            continue
        first_lba = _u64(entry, 32)
        last_lba = _u64(entry, 40)
        if first_lba > last_lba:
            raise GptError("partition %d has first_lba %d > last_lba %d"
                           % (index + 1, first_lba, last_lba))
        partitions.append({"index": index + 1, "first_lba": first_lba,
                           "last_lba": last_lba})
    return partitions


def _read_layout(handle, disk_size):
    if disk_size < SECTOR_SIZE * 4:
        raise GptError("disk is too small to hold a GPT (%d bytes)" % disk_size)
    if disk_size % SECTOR_SIZE:
        raise GptError("disk size %d is not a multiple of %d" % (disk_size, SECTOR_SIZE))
    total_sectors = disk_size // SECTOR_SIZE
    last_lba = total_sectors - 1

    mbr = _read_exact(handle, 0, SECTOR_SIZE)
    if mbr[510:512] != b"\x55\xaa":
        raise GptError("missing protective MBR signature")
    if mbr[MBR_PARTITION_OFFSET + 4] != PROTECTIVE_MBR_TYPE:
        raise GptError("not a protective MBR (partition type 0x%02x)"
                       % mbr[MBR_PARTITION_OFFSET + 4])
    mbr_start_lba = _u32(mbr, MBR_PARTITION_OFFSET + 8)
    mbr_sectors = _u32(mbr, MBR_PARTITION_OFFSET + 12)
    expected_mbr_sectors = min(last_lba, MBR_MAX_SECTORS)
    if mbr_start_lba != 1:
        raise GptError("protective MBR partition starts at LBA %d, expected LBA 1"
                       % mbr_start_lba)
    if mbr_sectors != expected_mbr_sectors:
        raise GptError(
            "protective MBR size %d does not match the disk's last LBA %d"
            % (mbr_sectors, expected_mbr_sectors))

    primary = _read_header(handle, 1)
    if primary["alternate_lba"] <= 1:
        raise GptError("primary GPT header has no alternate header")
    if primary["alternate_lba"] > last_lba:
        raise GptError("primary GPT alternate header LBA %d is beyond the disk end %d"
                       % (primary["alternate_lba"], last_lba))
    if primary["partition_entry_lba"] * SECTOR_SIZE + primary["entries_bytes"] > disk_size:
        raise GptError("primary GPT entry array extends beyond the disk")
    entries = _read_entries(handle, primary)
    partitions = _partition_extents(entries, primary["num_entries"], primary["entry_size"])
    if primary["last_usable_lba"] < primary["first_usable_lba"]:
        raise GptError("GPT last_usable_lba is before first_usable_lba")
    for partition in partitions:
        if partition["last_lba"] > primary["last_usable_lba"]:
            raise GptError("partition %d ends beyond last_usable_lba"
                           % partition["index"])

    alternate_lba = primary["alternate_lba"]
    backup = _read_header(handle, alternate_lba)
    if backup["alternate_lba"] != 1:
        raise GptError("backup GPT header does not point back to LBA 1")
    if backup["partition_entry_lba"] * SECTOR_SIZE + backup["entries_bytes"] > disk_size:
        raise GptError("backup GPT entry array extends beyond the disk")
    backup_entries = _read_entries(handle, backup)
    if backup_entries != entries:
        raise GptError("backup GPT partition entries differ from the primary")
    if (backup["first_usable_lba"] != primary["first_usable_lba"]
            or backup["num_entries"] != primary["num_entries"]
            or backup["entry_size"] != primary["entry_size"]):
        raise GptError("primary and backup GPT geometry disagree")

    return {
        "disk_size": disk_size,
        "total_sectors": total_sectors,
        "last_lba": last_lba,
        "mbr": mbr,
        "mbr_sectors": mbr_sectors,
        "primary": primary,
        "backup": backup,
        "entries": entries,
        "partitions": partitions,
    }


def inspect_disk(path):
    """Read and validate a raw GPT disk without writing to it."""
    path = Path(path)
    disk_size = path.stat().st_size
    with open(path, "rb") as handle:
        layout = _read_layout(handle, disk_size)
    return _summary(layout)


def _summary(layout):
    primary = layout["primary"]
    return {
        "disk_size": layout["disk_size"],
        "total_sectors": layout["total_sectors"],
        "last_lba": layout["last_lba"],
        "mbr_size_in_lba": layout["mbr_sectors"],
        "alternate_lba": primary["alternate_lba"],
        "last_usable_lba": primary["last_usable_lba"],
        "first_usable_lba": primary["first_usable_lba"],
        "num_entries": primary["num_entries"],
        "entry_size": primary["entry_size"],
        "partitions": [dict(item) for item in layout["partitions"]],
    }


def plan_expansion(layout, new_disk_size):
    """Return the bounded metadata writes that make the GPT describe new_disk_size."""
    if new_disk_size % SECTOR_SIZE:
        raise GptError("requested disk size %d is not a multiple of %d"
                       % (new_disk_size, SECTOR_SIZE))
    old_disk_size = layout["disk_size"]
    if new_disk_size < old_disk_size:
        raise GptError("refusing to shrink the disk from %d to %d bytes"
                       % (old_disk_size, new_disk_size))
    if new_disk_size == old_disk_size:
        raise GptError("requested disk size equals the current size; nothing to expand")
    new_last_lba = new_disk_size // SECTOR_SIZE - 1
    entry_sectors = (layout["primary"]["entries_bytes"] + SECTOR_SIZE - 1) // SECTOR_SIZE
    new_last_usable = new_last_lba - entry_sectors - 1
    current_last_usable = layout["primary"]["last_usable_lba"]
    if new_last_usable <= current_last_usable:
        raise GptError("requested disk leaves no room beyond last_usable_lba %d"
                       % current_last_usable)
    for partition in layout["partitions"]:
        if partition["last_lba"] > new_last_usable:
            raise GptError("partition %d would not fit in the expanded GPT"
                           % partition["index"])

    primary = bytearray(layout["primary"]["raw"])
    struct.pack_into("<Q", primary, 32, new_last_lba)
    struct.pack_into("<Q", primary, 48, new_last_usable)
    _set_header_crc(primary, layout["primary"]["header_size"])

    backup = bytearray(layout["backup"]["raw"])
    struct.pack_into("<Q", backup, 24, new_last_lba)
    struct.pack_into("<Q", backup, 32, 1)
    struct.pack_into("<Q", backup, 48, new_last_usable)
    struct.pack_into("<Q", backup, 72, new_last_lba - entry_sectors)
    _set_header_crc(backup, layout["backup"]["header_size"])

    mbr = bytearray(layout["mbr"])
    # The protective partition starts at LBA 1, so its sector count runs from
    # LBA 1 through the last LBA: the last LBA itself, capped for large disks.
    struct.pack_into("<I", mbr, MBR_PARTITION_OFFSET + 12,
                     min(new_last_lba, MBR_MAX_SECTORS))

    writes = [
        # The backup must exist before the primary header points at it.
        ((new_last_lba - entry_sectors) * SECTOR_SIZE, layout["entries"]),
        (new_last_lba * SECTOR_SIZE, bytes(backup)),
        (SECTOR_SIZE, bytes(primary)),
        (0, bytes(mbr)),
    ]
    written_bytes = sum(len(data) for _, data in writes)
    if written_bytes > MAX_ENTRY_ARRAY_BYTES + 2 * SECTOR_SIZE + SECTOR_SIZE:
        raise GptError("internal error: expansion writes are not bounded")
    for offset, data in writes:
        if offset < 0 or offset + len(data) > new_disk_size:
            raise GptError("internal error: expansion write falls outside the disk")
    return {
        "old_disk_size": old_disk_size,
        "new_disk_size": new_disk_size,
        "old_last_lba": layout["last_lba"],
        "new_last_lba": new_last_lba,
        "old_last_usable_lba": current_last_usable,
        "new_last_usable_lba": new_last_usable,
        "entry_sectors": entry_sectors,
        "writes": writes,
        "written_bytes": written_bytes,
    }


def expand_disk(path, new_disk_size):
    """Expand a raw GPT file in place and verify the result.

    The caller must pass a disposable copy, never the pinned base image. The
    write set is exactly the protective MBR, the primary header, the relocated
    backup entry array and the relocated backup header.
    """
    path = Path(path)
    current_size = path.stat().st_size
    with open(path, "r+b") as handle:
        layout = _read_layout(handle, current_size)
        if current_size == new_disk_size:
            if layout["primary"]["alternate_lba"] == new_disk_size // SECTOR_SIZE - 1:
                return _result("ALREADY_EXPANDED", layout, layout, 0)
            raise GptError("disk already has the target size but an inconsistent GPT")
        plan = plan_expansion(layout, new_disk_size)
        handle.truncate(new_disk_size)
        for offset, data in plan["writes"]:
            handle.seek(offset)
            handle.write(data)
        handle.flush()
        os.fsync(handle.fileno())

    with open(path, "rb") as handle:
        verified = _read_layout(handle, new_disk_size)
    expected_last = new_disk_size // SECTOR_SIZE - 1
    if verified["primary"]["alternate_lba"] != expected_last:
        raise GptError("expanded GPT does not point the alternate header at the new end")
    if verified["backup"]["my_lba"] != expected_last:
        raise GptError("expanded GPT backup header is not at the new end")
    if verified["primary"]["last_usable_lba"] != plan["new_last_usable_lba"]:
        raise GptError("expanded GPT last_usable_lba was not updated")
    if verified["partitions"] != layout["partitions"]:
        raise GptError("expanded GPT changed partition entries")
    result = _result("EXPANDED", layout, verified, plan["written_bytes"])
    result.update({
        "old_last_lba": plan["old_last_lba"],
        "new_last_lba": plan["new_last_lba"],
        "new_last_usable_lba": plan["new_last_usable_lba"],
    })
    return result


def _result(status, before, after, written_bytes):
    return {
        "status": status,
        "old_disk_bytes": before["disk_size"],
        "new_disk_bytes": after["disk_size"],
        "old_last_lba": before["last_lba"],
        "new_last_lba": after["last_lba"],
        "new_last_usable_lba": after["primary"]["last_usable_lba"],
        "partition_count": len(after["partitions"]),
        "written_bytes": written_bytes,
    }
