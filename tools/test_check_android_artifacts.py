import importlib.util
import io
import struct
import tempfile
import unittest
import zipfile
from pathlib import Path
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("check_android_artifacts", Path(__file__).with_name("check_android_artifacts.py"))
checker = importlib.util.module_from_spec(spec)
spec.loader.exec_module(checker)


def elf(abi, alignment=16384):
    elf_class, machine = checker.ABIS[abi]
    data = bytearray(128)
    data[:6] = b"\x7fELF" + bytes((elf_class, 1))
    struct.pack_into("<H", data, 18, machine)
    if elf_class == 2:
        struct.pack_into("<Q", data, 32, 64)
        struct.pack_into("<HH", data, 54, 56, 1)
        struct.pack_into("<IIQQQQQQ", data, 64, 1, 5, 0, 0, 0, 128, 128, alignment)
    else:
        struct.pack_into("<I", data, 28, 64)
        struct.pack_into("<HH", data, 42, 32, 1)
        struct.pack_into("<IIIIIIII", data, 64, 1, 0, 0, 0, 128, 128, 5, alignment)
    return bytes(data)


def java_class(methods):
    constants = []
    for name, descriptor in methods:
        constants.extend((name, descriptor))
    header = b"\xca\xfe\xba\xbe" + struct.pack(">HHH", 0, 52, len(constants) + 1)
    pool = b"".join(b"\x01" + struct.pack(">H", len(value)) + value.encode() for value in constants)
    # Only the structural class/member sections inspected by the checker are needed.
    body = struct.pack(">HHHHHH", 1, 0, 0, 0, 0, len(methods))
    for index in range(len(methods)):
        body += struct.pack(">HHHH", 1, index * 2 + 1, index * 2 + 2, 0)
    return header + pool + body + b"\x00\x00"


class ArtifactChecks(unittest.TestCase):
    def test_original_safety_contract_is_not_weakened(self):
        required = {b"pre-handshake connection rejected: reason=", b"kunbox_physical_dial_gate_v1",
                    b"kunbox_wireguard_physical_gate_v1", b"kunbox override destination:",
                    b"system TCP connection limit reached: active="}
        self.assertTrue(required.issubset(checker.REQUIRED_MARKERS))

    def test_elf_architecture_and_alignment(self):
        for abi in checker.ABIS:
            self.assertEqual(checker.inspect_elf(elf(abi), abi)["abi"], abi)
            minimum = 16384 if checker.ABIS[abi][0] == 2 else 4096
            with self.assertRaisesRegex(ValueError, "aligned"):
                checker.inspect_elf(elf(abi, minimum // 2), abi)
        with self.assertRaisesRegex(ValueError, "machine"):
            checker.inspect_elf(elf("arm64-v8a"), "x86_64")
        invalid = bytearray(elf("arm64-v8a"))
        struct.pack_into("<IIQQQQQQ", invalid, 64, 1, 5, 0, 0, 0, 129, 129, 16384)
        with self.assertRaisesRegex(ValueError, "bounds"):
            checker.inspect_elf(invalid, "arm64-v8a")
        invalid = bytearray(elf("arm64-v8a", 32768))
        struct.pack_into("<IIQQQQQQ", invalid, 64, 1, 5, 0, 16384, 16384, 128, 128, 32768)
        with self.assertRaisesRegex(ValueError, "aligned"):
            checker.inspect_elf(invalid, "arm64-v8a")

    def test_real_zip_and_missing_abi(self):
        classes = io.BytesIO()
        with zipfile.ZipFile(classes, "w") as archive:
            for name in ("CommandServer", "CommandClient", "PlatformInterface", "Libbox"):
                methods = {"CommandServer": [("pause", "()V"), ("wake", "()V"), ("resetNetwork", "()V")],
                           "PlatformInterface": [("useProcFS", "()Z")],
                           "Libbox": [("xrayVersion", "()Ljava/lang/String;")]}.get(name, [])
                archive.writestr("io/nekohasekai/libbox/" + name + ".class", java_class(methods))
        with tempfile.TemporaryDirectory() as temporary:
            path = Path(temporary) / "libbox.aar"
            for abis in (list(checker.ABIS), ["arm64-v8a"]):
                with zipfile.ZipFile(path, "w") as archive:
                    archive.writestr("classes.jar", classes.getvalue())
                    for abi in abis:
                        archive.writestr("jni/" + abi + "/libbox.so", elf(abi))
                if len(abis) == 4:
                    self.assertEqual(len(checker.inspect_aar(path)["nativeLibraries"]), 4)
                    with self.assertRaisesRegex(ValueError, "Missing safety marker"):
                        checker.inspect_aar(path, require_safety=True)
                    for forbidden in (False, True):
                        with zipfile.ZipFile(path, "w") as archive:
                            archive.writestr("classes.jar", classes.getvalue())
                            for abi in abis:
                                markers = b" ".join(checker.REQUIRED_MARKERS)
                                if forbidden:
                                    markers += checker.FORBIDDEN_MARKERS[0]
                                archive.writestr("jni/" + abi + "/libbox.so", elf(abi) + markers)
                        if forbidden:
                            with self.assertRaisesRegex(ValueError, "Forbidden implementation"):
                                checker.inspect_aar(path, require_safety=True)
                        else:
                            checker.inspect_aar(path, require_safety=True)
                else:
                    with self.assertRaisesRegex(ValueError, "exactly four"):
                        checker.inspect_aar(path)

    def test_java_method_requires_signature_not_string_presence(self):
        data = java_class([("pause", "(I)V"), ("wake", "()V")])
        methods = checker.public_methods(data)
        self.assertNotIn(("pause", "()V"), methods)
        self.assertIn(("wake", "()V"), methods)

    def test_source_digest_changes_without_commit(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            path = root / "source.go"
            path.write_text("package test\n")
            def fake_git(_, *args):
                if args[0] == "rev-parse":
                    return b"a" * 40
                if args[0] == "ls-files":
                    return b"source.go\0"
                return b" M source.go"
            with patch.object(checker, "git", fake_git):
                before = checker.source_identity(root)
                path.write_text("package changed\n")
                after = checker.source_identity(root)
            self.assertTrue(before["dirty"])
            self.assertTrue(after["dirty"])
            self.assertEqual(before["commit"], after["commit"])
            self.assertNotEqual(before["sourceDigest"], after["sourceDigest"])


if __name__ == "__main__":
    unittest.main()
