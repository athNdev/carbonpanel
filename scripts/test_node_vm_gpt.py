# SPDX-License-Identifier: MPL-2.0
import hashlib
import os
import struct
import tempfile
import unittest
from pathlib import Path

from scripts import node_vm_gpt as gpt

SECTOR = gpt.SECTOR_SIZE
ENTRY_COUNT = 128
ENTRY_SIZE = 128
ENTRY_SECTORS = ENTRY_COUNT * ENTRY_SIZE // SECTOR


def _crc32(data):
    return gpt._crc32(data)


def _header(*, my_lba, alternate_lba, first_usable, last_usable,
            entry_lba, entries_crc, header_size=92):
    raw = bytearray(SECTOR)
    raw[0:8] = gpt.GPT_SIGNATURE
    struct.pack_into("<I", raw, 8, gpt.GPT_REVISION)
    struct.pack_into("<I", raw, 12, header_size)
    struct.pack_into("<Q", raw, 24, my_lba)
    struct.pack_into("<Q", raw, 32, alternate_lba)
    struct.pack_into("<Q", raw, 40, first_usable)
    struct.pack_into("<Q", raw, 48, last_usable)
    raw[56:72] = bytes(range(16))
    struct.pack_into("<Q", raw, 72, entry_lba)
    struct.pack_into("<I", raw, 80, ENTRY_COUNT)
    struct.pack_into("<I", raw, 84, ENTRY_SIZE)
    struct.pack_into("<I", raw, 88, entries_crc)
    gpt._set_header_crc(raw, header_size)
    return bytes(raw)


def _entries(partitions):
    data = bytearray(ENTRY_COUNT * ENTRY_SIZE)
    for index, (first_lba, last_lba) in enumerate(partitions):
        base = index * ENTRY_SIZE
        data[base:base + 16] = b"\x0c" + b"\x00" * 15
        data[base + 16:base + 32] = bytes([index + 1]) + b"\x00" * 15
        struct.pack_into("<Q", data, base + 32, first_lba)
        struct.pack_into("<Q", data, base + 40, last_lba)
        struct.pack_into("<Q", data, base + 48, 0)
        name = ("partition-%d" % (index + 1)).encode("utf-16-le")
        data[base + 56:base + 56 + len(name)] = name
    return bytes(data)


def _write_gpt(path, total_bytes, partitions, alternate_lba=None, last_usable=None,
               mbr_start_lba=1, mbr_size_in_lba=None):
    assert total_bytes % SECTOR == 0
    total_sectors = total_bytes // SECTOR
    last_lba = total_sectors - 1
    entry_sectors = ENTRY_SECTORS
    first_usable = 2 + entry_sectors
    if last_usable is None:
        last_usable = last_lba - entry_sectors - 1
    entries = _entries(partitions)
    entries_crc = _crc32(entries)
    if alternate_lba is None:
        alternate_lba = last_lba

    if mbr_size_in_lba is None:
        mbr_size_in_lba = min(last_lba, gpt.MBR_MAX_SECTORS)
    mbr = bytearray(SECTOR)
    mbr[gpt.MBR_PARTITION_OFFSET + 4] = gpt.PROTECTIVE_MBR_TYPE
    struct.pack_into("<I", mbr, gpt.MBR_PARTITION_OFFSET + 8, mbr_start_lba)
    struct.pack_into("<I", mbr, gpt.MBR_PARTITION_OFFSET + 12, mbr_size_in_lba)
    mbr[510:512] = b"\x55\xaa"

    primary = _header(my_lba=1, alternate_lba=alternate_lba,
                      first_usable=first_usable, last_usable=last_usable,
                      entry_lba=2, entries_crc=entries_crc)
    backup = _header(my_lba=alternate_lba, alternate_lba=1,
                     first_usable=first_usable, last_usable=last_usable,
                     entry_lba=alternate_lba - entry_sectors,
                     entries_crc=entries_crc)

    with open(path, "wb") as handle:
        handle.truncate(total_bytes)
        handle.seek(0)
        handle.write(mbr)
        handle.seek(1 * SECTOR)
        handle.write(primary)
        handle.seek(2 * SECTOR)
        handle.write(entries)
        handle.seek((alternate_lba - entry_sectors) * SECTOR)
        handle.write(entries)
        handle.seek(alternate_lba * SECTOR)
        handle.write(backup)
    return {
        "total_bytes": total_bytes,
        "last_lba": last_lba,
        "first_usable": first_usable,
        "last_usable": last_usable,
        "alternate_lba": alternate_lba,
        "entries": entries,
        "entries_crc": entries_crc,
    }


def _bytes_at(path, offset, length):
    with open(path, "rb") as handle:
        handle.seek(offset)
        return handle.read(length)


def _digest(path):
    return hashlib.sha256(Path(path).read_bytes()).hexdigest()


class ParseSizeTests(unittest.TestCase):
    def test_accepts_bytes_and_binary_suffixes(self):
        self.assertEqual(gpt.parse_disk_size(1024), 1024)
        self.assertEqual(gpt.parse_disk_size("1024"), 1024)
        self.assertEqual(gpt.parse_disk_size("20G"), 20 * 1024 ** 3)
        self.assertEqual(gpt.parse_disk_size("20480M"), 20 * 1024 ** 3)
        self.assertEqual(gpt.parse_disk_size("1g"), 1024 ** 3)
        self.assertEqual(gpt.parse_disk_size("2GB"), 2 * 1024 ** 3)

    def test_rejects_invalid_sizes(self):
        for value in ("", "0", "-1G", "20 GiB", "1.5G", "20X", "G", None, 0, -5, True):
            with self.subTest(value=value):
                with self.assertRaises(gpt.GptError):
                    gpt.parse_disk_size(value)

    def test_twenty_gib_matches_the_retained_arm64_geometry(self):
        size = gpt.parse_disk_size("20G")
        self.assertEqual(size, 21_474_836_480)
        self.assertEqual(size // SECTOR - 1, 41_943_039)
        self.assertEqual(3_758_096_384 // SECTOR - 1, 7_340_031)


class ExpandTests(unittest.TestCase):
    OLD = 8 * 1024 * 1024
    NEW = 16 * 1024 * 1024

    def _disk(self, directory, old=None, partitions=((2048, 4096), (6144, 8192))):
        path = Path(directory) / "disk.raw"
        layout = _write_gpt(path, old or self.OLD, partitions)
        return path, layout

    def test_moves_the_backup_header_to_the_new_end(self):
        with tempfile.TemporaryDirectory() as directory:
            path, before = self._disk(directory)
            result = gpt.expand_disk(path, self.NEW)

            self.assertEqual(path.stat().st_size, self.NEW)
            self.assertEqual(result["status"], "EXPANDED")
            self.assertEqual(result["old_last_lba"], before["last_lba"])
            self.assertEqual(result["new_last_lba"], self.NEW // SECTOR - 1)
            inspection = gpt.inspect_disk(path)
            self.assertEqual(inspection["mbr_size_in_lba"], self.NEW // SECTOR - 1)
            self.assertEqual(inspection["alternate_lba"], self.NEW // SECTOR - 1)
            self.assertEqual(inspection["last_usable_lba"],
                             self.NEW // SECTOR - 1 - ENTRY_SECTORS - 1)
            self.assertEqual(inspection["partitions"],
                             [{"index": 1, "first_lba": 2048, "last_lba": 4096},
                              {"index": 2, "first_lba": 6144, "last_lba": 8192}])

            # The relocated backup header and entry array are valid and at the new end.
            new_last = self.NEW // SECTOR - 1
            header = _bytes_at(path, new_last * SECTOR, SECTOR)
            self.assertEqual(header[:8], gpt.GPT_SIGNATURE)
            self.assertEqual(struct.unpack_from("<Q", header, 24)[0], new_last)
            self.assertEqual(struct.unpack_from("<Q", header, 32)[0], 1)
            backup_entries = _bytes_at(path, (new_last - ENTRY_SECTORS) * SECTOR,
                                       ENTRY_SECTORS * SECTOR)
            self.assertEqual(backup_entries, before["entries"])

    def test_protective_mbr_is_sized_to_the_expanded_disk_last_lba(self):
        with tempfile.TemporaryDirectory() as directory:
            path, _ = self._disk(directory)
            gpt.expand_disk(path, self.NEW)
            expected = self.NEW // SECTOR - 1
            mbr = _bytes_at(path, 0, SECTOR)
            self.assertEqual(struct.unpack_from(
                "<I", mbr, gpt.MBR_PARTITION_OFFSET + 8)[0], 1)
            self.assertEqual(struct.unpack_from(
                "<I", mbr, gpt.MBR_PARTITION_OFFSET + 12)[0], expected)
            self.assertEqual(gpt.inspect_disk(path)["mbr_size_in_lba"], expected)

    def test_twenty_gib_plan_sizes_the_pmbr_to_the_last_lba(self):
        # Plan-only: applying it would allocate a 20 GiB fixture on constrained
        # runners. The 16 MiB case above exercises the actual write and read-back.
        with tempfile.TemporaryDirectory() as directory:
            path, _ = self._disk(directory)
            target = gpt.parse_disk_size("20G")
            expected = 41_943_039
            self.assertEqual(target // SECTOR - 1, expected)
            with open(path, "rb") as handle:
                parsed = gpt._read_layout(handle, path.stat().st_size)
            plan = gpt.plan_expansion(parsed, target)
            mbr = dict(plan["writes"])[0]
            self.assertEqual(plan["new_last_lba"], expected)
            self.assertEqual(struct.unpack_from(
                "<I", mbr, gpt.MBR_PARTITION_OFFSET + 12)[0], expected)

    def test_leaves_partition_data_and_the_primary_entry_array_untouched(self):
        with tempfile.TemporaryDirectory() as directory:
            path, before = self._disk(directory)
            data_offset = 2048 * SECTOR
            data_before = _bytes_at(path, data_offset, 8192 * SECTOR)
            entries_before = _bytes_at(path, 2 * SECTOR, ENTRY_SECTORS * SECTOR)
            gpt.expand_disk(path, self.NEW)
            self.assertEqual(_bytes_at(path, data_offset, 8192 * SECTOR), data_before)
            self.assertEqual(_bytes_at(path, 2 * SECTOR, ENTRY_SECTORS * SECTOR),
                             entries_before)
            self.assertEqual(before["entries"], entries_before)
            # The old backup header remains readable but is no longer referenced.
            old_backup = _bytes_at(path, before["alternate_lba"] * SECTOR, SECTOR)
            self.assertEqual(old_backup[:8], gpt.GPT_SIGNATURE)

    def test_writes_only_bounded_gpt_metadata(self):
        with tempfile.TemporaryDirectory() as directory:
            path, _ = self._disk(directory)
            layout = gpt.inspect_disk(path)
            with open(path, "rb") as handle:
                parsed = gpt._read_layout(handle, layout["disk_size"])
            plan = gpt.plan_expansion(parsed, self.NEW)
            self.assertLessEqual(plan["written_bytes"], 64 * 1024)
            for offset, data in plan["writes"]:
                self.assertGreaterEqual(offset, 0)
                self.assertLessEqual(offset + len(data), self.NEW)
            result = gpt.expand_disk(path, self.NEW)
            self.assertEqual(result["written_bytes"], plan["written_bytes"])

    def test_expansion_is_idempotent_without_rewriting_bytes(self):
        with tempfile.TemporaryDirectory() as directory:
            path, _ = self._disk(directory)
            gpt.expand_disk(path, self.NEW)
            digest = _digest(path)
            result = gpt.expand_disk(path, self.NEW)
            self.assertEqual(result["status"], "ALREADY_EXPANDED")
            self.assertEqual(result["written_bytes"], 0)
            self.assertEqual(_digest(path), digest)

    def test_refuses_to_shrink_and_preserves_the_file(self):
        with tempfile.TemporaryDirectory() as directory:
            path, _ = self._disk(directory)
            digest = _digest(path)
            with self.assertRaisesRegex(gpt.GptError, "shrink"):
                gpt.expand_disk(path, 4 * 1024 * 1024)
            self.assertEqual(path.stat().st_size, self.OLD)
            self.assertEqual(_digest(path), digest)

    def test_refuses_a_target_with_no_usable_space_gain(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "disk.raw"
            # A non-standard table whose last_usable_lba sits one sector below
            # the disk end leaves no room for a 33-sector backup reservation.
            _write_gpt(path, self.OLD, ((2048, 4096),),
                       last_usable=self.OLD // SECTOR - 2)
            with self.assertRaisesRegex(gpt.GptError, "no room beyond last_usable_lba"):
                gpt.expand_disk(path, self.OLD + SECTOR)
            self.assertEqual(path.stat().st_size, self.OLD)

    def test_refuses_unaligned_targets(self):
        with tempfile.TemporaryDirectory() as directory:
            path, _ = self._disk(directory)
            with self.assertRaisesRegex(gpt.GptError, "multiple of 512"):
                gpt.expand_disk(path, self.NEW + 1)

    def test_refuses_a_partition_that_would_not_fit(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "disk.raw"
            layout = _write_gpt(path, self.OLD, ((2048, 16384),))
            # Pretend the recorded partition extends to the old usable edge, then
            # ask for a tiny growth that cannot contain it in the new table.
            with open(path, "r+b") as handle:
                entries = bytearray(layout["entries"])
                struct.pack_into("<Q", entries, 40, layout["last_usable"] + 10)
                crc = _crc32(entries)
                handle.seek(2 * SECTOR)
                handle.write(entries)
                handle.seek((layout["alternate_lba"] - ENTRY_SECTORS) * SECTOR)
                handle.write(entries)
                for lba, alternate, entry_lba in (
                        (1, layout["alternate_lba"], 2),
                        (layout["alternate_lba"], 1,
                         layout["alternate_lba"] - ENTRY_SECTORS)):
                    header = _header(my_lba=lba, alternate_lba=alternate,
                                     first_usable=layout["first_usable"],
                                     last_usable=layout["last_usable"],
                                     entry_lba=entry_lba, entries_crc=crc)
                    handle.seek(lba * SECTOR)
                    handle.write(header)
            with self.assertRaisesRegex(gpt.GptError, "ends beyond last_usable_lba"):
                gpt.inspect_disk(path)


class RefusalTests(unittest.TestCase):
    OLD = 8 * 1024 * 1024

    def _broken(self, directory, mutate, partitions=((2048, 4096),)):
        path = Path(directory) / "disk.raw"
        info = _write_gpt(path, self.OLD, partitions)
        mutate(path, info)
        return path

    def test_refuses_a_non_gpt_disk(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "disk.raw"
            path.write_bytes(b"\x00" * self.OLD)
            with self.assertRaisesRegex(gpt.GptError, "protective MBR"):
                gpt.inspect_disk(path)

    def test_refuses_a_missing_protective_mbr_signature(self):
        def mutate(path, info):
            with open(path, "r+b") as handle:
                handle.seek(510)
                handle.write(b"\x00\x00")
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(gpt.GptError, "MBR signature"):
                gpt.inspect_disk(self._broken(directory, mutate))

    def test_refuses_a_protective_mbr_that_does_not_start_at_lba_1(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "disk.raw"
            _write_gpt(path, self.OLD, ((2048, 4096),), mbr_start_lba=0)
            with self.assertRaisesRegex(
                    gpt.GptError, "starts at LBA 0, expected LBA 1"):
                gpt.inspect_disk(path)

    def test_refuses_a_protective_mbr_that_is_one_sector_too_long(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "disk.raw"
            _write_gpt(path, self.OLD, ((2048, 4096),),
                       mbr_size_in_lba=self.OLD // SECTOR)
            with self.assertRaisesRegex(
                    gpt.GptError,
                    "protective MBR size .* does not match the disk's last LBA"):
                gpt.inspect_disk(path)

    def test_refuses_a_corrupt_primary_header_crc(self):
        def mutate(path, info):
            with open(path, "r+b") as handle:
                handle.seek(SECTOR + 16)
                handle.write(b"\x00\x00\x00\x00")
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(gpt.GptError, "primary|header CRC"):
                gpt.inspect_disk(self._broken(directory, mutate))

    def test_refuses_corrupt_backup_entries(self):
        def mutate(path, info):
            with open(path, "r+b") as handle:
                handle.seek((info["alternate_lba"] - ENTRY_SECTORS) * SECTOR)
                handle.write(b"\xff" * 16)
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(gpt.GptError, "CRC mismatch|differ"):
                gpt.inspect_disk(self._broken(directory, mutate))

    def test_refuses_a_backup_header_at_the_wrong_lba(self):
        def mutate(path, info):
            lba = info["alternate_lba"]
            with open(path, "r+b") as handle:
                handle.seek(lba * SECTOR)
                header = bytearray(handle.read(SECTOR))
                struct.pack_into("<Q", header, 24, lba - 1)
                gpt._set_header_crc(header, 92)
                handle.seek(lba * SECTOR)
                handle.write(header)
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaisesRegex(gpt.GptError, "my_lba"):
                gpt.inspect_disk(self._broken(directory, mutate))

    def test_refuses_an_unaligned_disk_size(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / "disk.raw"
            _write_gpt(path, self.OLD, ((2048, 4096),))
            with open(path, "ab") as handle:
                handle.write(b"\x00")
            with self.assertRaisesRegex(gpt.GptError, "multiple of 512"):
                gpt.inspect_disk(path)

    def test_failed_expansion_leaves_the_disk_size_unchanged(self):
        def mutate(path, info):
            with open(path, "r+b") as handle:
                handle.seek((info["alternate_lba"] - ENTRY_SECTORS) * SECTOR)
                handle.write(b"\xff" * 16)
        with tempfile.TemporaryDirectory() as directory:
            path = self._broken(directory, mutate)
            with self.assertRaises(gpt.GptError):
                gpt.expand_disk(path, 16 * 1024 * 1024)
            self.assertEqual(path.stat().st_size, self.OLD)


if __name__ == "__main__":
    unittest.main()
