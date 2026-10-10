#!/usr/bin/env python3
"""Check downloadable archives before publishing container images or a release."""

import hashlib
from pathlib import Path, PurePosixPath
import re
import sys
import tarfile


def check(root):
    expected = {f"dispatch{kind}-linux-{arch}.tar.gz"
                for kind in ("", "-platform") for arch in ("amd64", "arm64")}
    expected.add("dispatch-hosted-config.tar.gz")
    sums = {}
    for line in (root / "SHA256SUMS").read_text().splitlines():
        match = re.fullmatch(r"([a-f0-9]{64})  ([A-Za-z0-9._-]+)", line)
        if match is None or match[2] in sums:
            raise ValueError("invalid or duplicate checksum entry")
        sums[match[2]] = match[1]
    if set(sums) - expected - {"images.txt"} or expected - set(sums):
        raise ValueError("release checksums do not cover all expected assets")
    for name, digest in sums.items():
        actual = hashlib.sha256()
        with (root / name).open("rb") as asset:
            for block in iter(lambda: asset.read(1024 * 1024), b""):
                actual.update(block)
        if actual.hexdigest() != digest:
            raise ValueError(f"checksum mismatch: {name}")
    for name in sorted(expected):
        with tarfile.open(root / name, "r:gz") as archive:
            entries = {}
            for entry in archive:
                path = PurePosixPath(entry.name)
                if path.is_absolute() or ".." in path.parts or entry.name in entries:
                    raise ValueError(f"unsafe or duplicate archive path: {name}: {entry.name}")
                if not entry.isfile() and not entry.isdir():
                    raise ValueError(f"unexpected archive entry type: {name}: {entry.name}")
                entries[entry.name] = entry
            if name == "dispatch-hosted-config.tar.gz":
                for service in ("platform", "dns", "worker"):
                    for path in (f"deploy/hosted/dispatch-{service}.service", f"deploy/hosted/{service}.env.example"):
                        if path not in entries:
                            raise ValueError(f"missing installation file: {path}")
                continue
            arch = name.removesuffix(".tar.gz").rsplit("-", 1)[1]
            binaries = {"dispatch": arch, "dispatchctl": arch}
            if name.startswith("dispatch-platform-"):
                binaries = {binary: arch for binary in
                            ("dispatch-platform", "dispatch-dns", "dispatch-worker", "dispatch-certificate-sync")}
                binaries.update({f"edge/linux-{target}": target for target in ("amd64", "arm64")})
            for binary, target in binaries.items():
                entry = entries.get(binary)
                if entry is None or not entry.isfile() or not entry.mode & 0o111:
                    raise ValueError(f"missing executable: {name}: {binary}")
                header = archive.extractfile(entry).read(20)
                machine = {"amd64": 62, "arm64": 183}[target]
                if header[:6] != b"\x7fELF\x02\x01" or int.from_bytes(header[18:20], "little") != machine:
                    raise ValueError(f"wrong executable architecture: {name}: {binary}")
            for license_file in ("LICENSE", "OFL-Manrope.txt"):
                if license_file not in entries:
                    raise ValueError(f"missing license: {name}: {license_file}")
    print("Release checksums, archive paths and executable architectures passed.")


if __name__ == "__main__":
    try:
        check(Path(sys.argv[1] if len(sys.argv) > 1 else "release"))
    except (OSError, ValueError, tarfile.TarError) as error:
        sys.exit(str(error))
