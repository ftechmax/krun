#!/usr/bin/env python3
"""Verify a Windows PE carries an RT_MANIFEST resource requesting administrator
rights. Replaces the Windows-SDK mt.exe extraction the build used to need, so
the check runs anywhere Python does."""
import struct
import sys

RT_MANIFEST = 24


def resource_type_ids(data):
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    if data[pe:pe + 4] != b"PE\0\0":
        raise SystemExit("not a PE image")
    section_count = struct.unpack_from("<H", data, pe + 6)[0]
    opt_size = struct.unpack_from("<H", data, pe + 20)[0]
    sections = pe + 24 + opt_size

    for i in range(section_count):
        header = sections + 40 * i
        if data[header:header + 8].rstrip(b"\0") != b".rsrc":
            continue
        raw_ptr = struct.unpack_from("<I", data, header + 20)[0]
        named, by_id = struct.unpack_from("<HH", data, raw_ptr + 12)
        return [
            struct.unpack_from("<II", data, raw_ptr + 16 + 8 * j)[0]
            for j in range(named + by_id)
        ]
    raise SystemExit("no .rsrc section: the UAC manifest resource is missing")


def main(path):
    data = open(path, "rb").read()
    if RT_MANIFEST not in resource_type_ids(data):
        raise SystemExit(f"{path}: no RT_MANIFEST resource")
    if b'level="requireAdministrator"' not in data:
        raise SystemExit(f"{path}: manifest does not requireAdministrator")
    print(f"{path}: RT_MANIFEST present, requireAdministrator")


if __name__ == "__main__":
    main(sys.argv[1])
