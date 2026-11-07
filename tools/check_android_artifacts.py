"""Inspect actual AAR contents and capture the local source identity (stdlib only)."""
import argparse
import hashlib
import io
import json
import re
import struct
import subprocess
import zipfile
from pathlib import Path

ABIS = {"arm64-v8a": (2, 183), "armeabi-v7a": (1, 40), "x86": (1, 3), "x86_64": (2, 62)}
REQUIRED_MARKERS = (b"pre-handshake connection rejected: reason=", b"kunbox_physical_dial_gate_v1",
                    b"kunbox_wireguard_physical_gate_v1", b"kunbox override destination:",
                    b"system TCP connection limit reached: active=")
FORBIDDEN_MARKERS = (b"controlplane.tailscale.com", b"Tailscale outbound",
                     b"github.com/roseforljh/RayBox-rx/protocol/vless/encryption", b"parseVLESSClientEncryption",
                     *(prefix + name for prefix in (b"experimental/libbox.", b"sdk/android.") for name in
                       (b"CheckNetworkRecoveryNeeded", b"CloseAllTrackedConnections", b"CloseIdleConnections",
                        b"GetConnectionCount", b"RecoverNetworkAuto", b"ResetAllConnections")))


def git(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args])


def source_identity(root):
    commit = git(root, "rev-parse", "HEAD").decode().strip()
    if not re.fullmatch(r"[0-9a-f]{40}", commit):
        raise ValueError("Source commit must be a full Git SHA")
    names = git(root, "ls-files", "--cached", "--others", "--exclude-standard", "-z")
    digest = hashlib.sha256()
    for name in sorted(set(names.split(b"\0")) - {b""}):
        path = root / name.decode("utf-8")
        digest.update(name + b"\0")
        if path.is_symlink():
            data = path.readlink().as_posix().encode()
            digest.update(b"symlink\0" + hashlib.sha256(data).digest())
        elif path.is_file():
            executable = path.stat().st_mode & 0o111
            digest.update(b"exec\0" if executable else b"file\0")
            digest.update(hashlib.sha256(path.read_bytes()).digest())
        elif not path.exists():
            digest.update(b"deleted\0")
        else:
            raise ValueError("Unexpected non-file source entry: " + str(path))
    return {"commit": commit, "dirty": bool(git(root, "status", "--porcelain", "--untracked-files=all")),
            "sourceDigest": digest.hexdigest(), "sourceDigestAlgorithm": "sha256-git-path-kind-content-v1"}


def inspect_elf(data, abi):
    elf_class, machine = ABIS[abi]
    if len(data) < 64 or data[:6] != b"\x7fELF" + bytes((elf_class, 1)):
        raise ValueError("Unexpected ELF class or byte order: " + abi)
    if struct.unpack_from("<H", data, 18)[0] != machine:
        raise ValueError("Unexpected ELF machine: " + abi)
    if elf_class == 2:
        phoff = struct.unpack_from("<Q", data, 32)[0]
        phsize, count = struct.unpack_from("<HH", data, 54)
        layout, minimum = "<IIQQQQQQ", 56
    else:
        phoff = struct.unpack_from("<I", data, 28)[0]
        phsize, count = struct.unpack_from("<HH", data, 42)
        layout, minimum = "<IIIIIIII", 32
    if not count or phsize < minimum or phoff + phsize * count > len(data):
        raise ValueError("Invalid ELF program headers: " + abi)
    alignments = []
    for index in range(count):
        fields = struct.unpack_from(layout, data, phoff + index * phsize)
        if fields[0] != 1:
            continue
        offset, address = (fields[2], fields[3]) if elf_class == 2 else (fields[1], fields[2])
        alignment = fields[7]
        file_size, memory_size = (fields[5], fields[6]) if elf_class == 2 else (fields[4], fields[5])
        if file_size > memory_size or offset + file_size > len(data):
            raise ValueError("Invalid ELF load segment bounds: " + abi)
        page_size = 16384 if elf_class == 2 else 4096
        if (alignment < page_size or alignment & (alignment - 1) or
                offset % alignment != address % alignment):
            raise ValueError("ELF load segment is not aligned to " + str(page_size) + " bytes: " + abi)
        alignments.append(alignment)
    if not alignments:
        raise ValueError("ELF has no load segments: " + abi)
    return {"abi": abi, "machine": machine, "loadAlignments": alignments,
            "size": len(data), "sha256": hashlib.sha256(data).hexdigest()}


def public_methods(data):
    if data[:4] != b"\xca\xfe\xba\xbe":
        raise ValueError("Invalid Java class header")
    offset = 10
    pool = {}
    index = 1
    count = struct.unpack_from(">H", data, 8)[0]
    widths = {3: 4, 4: 4, 5: 8, 6: 8, 7: 2, 8: 2, 9: 4, 10: 4, 11: 4, 12: 4,
              15: 3, 16: 2, 17: 4, 18: 4, 19: 2, 20: 2}
    while index < count:
        tag = data[offset]
        offset += 1
        if tag == 1:
            length = struct.unpack_from(">H", data, offset)[0]
            offset += 2
            pool[index] = data[offset:offset + length].decode("utf-8", errors="replace")
            offset += length
        else:
            if tag not in widths:
                raise ValueError("Unknown Java constant pool tag: " + str(tag))
            offset += widths[tag]
            if tag in (5, 6):
                index += 1
        index += 1
    interfaces = struct.unpack_from(">H", data, offset + 6)[0]
    offset += 8 + interfaces * 2
    methods = set()
    for section in ("fields", "methods"):
        count = struct.unpack_from(">H", data, offset)[0]
        offset += 2
        for _ in range(count):
            access, name, descriptor, attributes = struct.unpack_from(">HHHH", data, offset)
            offset += 8
            if section == "methods" and access & 1:
                methods.add((pool[name], pool[descriptor]))
            for _ in range(attributes):
                length = struct.unpack_from(">I", data, offset + 2)[0]
                offset += 6 + length
    return methods


def inspect_aar(path, require_safety=False, expected_abis=None):
    expected_abis = set(ABIS) if expected_abis is None else set(expected_abis)
    if not expected_abis or not expected_abis.issubset(ABIS):
        raise ValueError("Unsupported ABI selection")
    data = path.read_bytes()
    native = []
    with zipfile.ZipFile(io.BytesIO(data)) as archive:
        names = archive.namelist()
        if len(names) != len(set(names)):
            raise ValueError("Duplicate ZIP entries: " + str(path))
        entries = {name.split("/")[1]: name for name in names
                   if re.fullmatch(r"jni/[^/]+/libraybox\.so", name)}
        if set(entries) != expected_abis:
            raise ValueError("AAR ABIs differ from expected selection: " + str(sorted(entries)))
        for abi in ABIS:
            if abi not in expected_abis:
                continue
            library = archive.read(entries[abi])
            native.append(inspect_elf(library, abi))
            if require_safety:
                for marker in REQUIRED_MARKERS:
                    if marker not in library:
                        raise ValueError("Missing safety marker for " + abi + ": " + marker.decode())
                for marker in FORBIDDEN_MARKERS:
                    if marker in library:
                        raise ValueError("Forbidden implementation for " + abi + ": " + marker.decode())
        with zipfile.ZipFile(io.BytesIO(archive.read("classes.jar"))) as classes:
            required = {"CommandServer", "CommandClient", "PlatformInterface", "Raybox"}
            for name in required:
                class_data = classes.read("io/raybox/raybox/" + name + ".class")
                if class_data[:4] != b"\xca\xfe\xba\xbe":
                    raise ValueError("Invalid Java class: " + name)
            server = public_methods(classes.read("io/raybox/raybox/CommandServer.class"))
            for method in ("pause", "wake", "resetNetwork"):
                if (method, "()V") not in server:
                    raise ValueError("Missing CommandServer method: " + method)
            platform = public_methods(classes.read("io/raybox/raybox/PlatformInterface.class"))
            if ("useProcFS", "()Z") not in platform:
                raise ValueError("Missing RayBox PlatformInterface.useProcFS")
            raybox = public_methods(classes.read("io/raybox/raybox/Raybox.class"))
            if ("version", "()Ljava/lang/String;") not in raybox:
                raise ValueError("Missing RayBox.version")
    return {"name": path.name, "size": len(data), "sha256": hashlib.sha256(data).hexdigest(),
            "nativeLibraries": native}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("mode", choices=("source", "aar"))
    parser.add_argument("paths", nargs="+", type=Path)
    parser.add_argument("--require-safety", action="store_true")
    args = parser.parse_args()
    if args.mode == "source":
        if len(args.paths) != 1:
            parser.error("source requires one repository root")
        result = source_identity(args.paths[0])
    else:
        result = [inspect_aar(path, require_safety=args.require_safety) for path in args.paths]
    print(json.dumps(result, indent=2))


if __name__ == "__main__":
    main()
