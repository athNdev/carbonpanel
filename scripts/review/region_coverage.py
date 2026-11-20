# SPDX-License-Identifier: MPL-2.0
"""Count persisted chunks in the exact region grid the fixture force-loads.

The four bounded forceload batches cover world chunks x=-16..15, z=-16..15,
which maps to region files r.{-1,0}.{-1,0}.mca. An .mca header has 1024
four-byte entries indexed as 4 * (x + 32 * z) within that region; a non-zero
sector count means the chunk is persisted. Only the requested chunk indices
count: entries outside the 32x32 grid must never qualify the fixture.
"""

import os


def requested_chunks(region_dir: str) -> tuple[int, int]:
    """Return (covered requested chunks, total region bytes) for the fixture."""
    covered = size = 0
    for rx in (-1, 0):
        xs = range(16, 32) if rx == -1 else range(0, 16)
        for rz in (-1, 0):
            zs = range(16, 32) if rz == -1 else range(0, 16)
            path = os.path.join(region_dir, f"r.{rx}.{rz}.mca")
            try:
                size += os.path.getsize(path)
                with open(path, "rb") as handle:
                    header = handle.read(4096)
            except OSError:
                continue
            if len(header) < 4096:
                continue
            for x in xs:
                for z in zs:
                    if header[4 * (x + 32 * z) + 3] != 0:
                        covered += 1
    return covered, size


def main() -> None:
    import argparse

    parser = argparse.ArgumentParser()
    parser.add_argument("--region-dir", required=True)
    args = parser.parse_args()
    covered, size = requested_chunks(args.region_dir)
    print(f"{covered}:{size}")


if __name__ == "__main__":
    main()
