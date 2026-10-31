"""Installer terminal tests: python3 scripts/test_install.py.

Run the published shell script through a pipe in a real pseudo-terminal,
with signed fixture downloads. Only the OS/mount inventory and the final
pairing binary are fixtures; curl, signature verification and filesystem
operations are real. Full daemon/service coverage lives in LinuxUpdate.
"""

import errno
import hashlib
import os
from pathlib import Path
import pty
import select
import signal
import subprocess
import tempfile
import time
import unittest


class InstallerTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name).resolve()
        self.home = self.root / "home"
        self.home.mkdir()
        self.entry = self.home / ".local/share/hermesh"
        self.mock = self.root / "commands"
        self.mock.mkdir()
        self.release = self.root / "release"
        self.release.mkdir()
        self.env = {k: v for k, v in os.environ.items()
                    if not k.startswith(("HERMESH_", "XDG_")) and k != "NO_COLOR"}
        self.env.update(HOME=str(self.home), TERM="xterm-256color",
                        PATH=f"{self.mock}:{os.environ['PATH']}",
                        HERMESH_DOWNLOAD_BASE=self.release.as_uri())
        self.command("uname", 'case "$1" in -s) echo Linux;; -m) echo x86_64;; esac')
        self.command("id", 'echo 1000')
        self.command("df", 'printf "Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 100000000 0 90000000 0%% /\\n"')

        key = self.root / "key.pem"
        subprocess.run(["openssl", "ecparam", "-name", "prime256v1", "-genkey",
                        "-noout", "-out", str(key)], check=True, capture_output=True)
        pub = subprocess.check_output(["openssl", "pkey", "-in", str(key), "-pubout"],
                                      stderr=subprocess.DEVNULL).decode().strip()
        fixture = b'#!/bin/sh\nprintf "PAIRING %s\\n" "$*"\n'
        sums = ""
        for binary in ("hermeshd", "hermesh"):
            name = f"{binary}-linux-amd64"
            (self.release / name).write_bytes(fixture)
            sums += f"{hashlib.sha256(fixture).hexdigest()}  {name}\n"
        (self.release / "checksums.txt").write_text(sums)
        subprocess.run(["openssl", "dgst", "-sha256", "-sign", str(key), "-out",
                        str(self.release / "checksums.txt.sig"),
                        str(self.release / "checksums.txt")], check=True, capture_output=True)
        self.script = self.root / "install.sh"
        self.source = Path(__file__).with_name("install.sh").read_text().replace(
            "__HERMESH_RELEASE_PUBKEY__", pub)
        self.source = self.source.replace("/etc.defaults/VERSION", str(self.root / "VERSION"))
        self.source = self.source.replace("/proc/mounts", str(self.root / "mounts"))
        # Relocate NAS fixtures without requiring root or mounting host disks.
        self.source = self.source.replace(r"\/volume", str(self.root).replace("/", r"\/") + r"\/volume")

    def command(self, name, body):
        path = self.mock / name
        path.write_text("#!/bin/sh\n" + body + "\n")
        path.chmod(0o755)

    def run_install(self, responses=(), *, terminal=True, args=""):
        self.script.write_text(self.source)
        command = f'cat "{self.script}" | sh -s -- {args}'
        if not terminal:
            result = subprocess.run(["sh", "-c", command], env=self.env,
                                    start_new_session=True, capture_output=True, timeout=15)
            return result.returncode, (result.stdout + result.stderr).decode()
        pid, fd = pty.fork()
        if pid == 0:
            os.execve("/bin/sh", ["sh", "-c", command], self.env)
        output = b""
        remaining = list(responses)
        consumed = 0
        deadline = time.monotonic() + 15
        try:
            while time.monotonic() < deadline:
                if select.select([fd], [], [], 0.1)[0]:
                    try:
                        chunk = os.read(fd, 65536)
                    except OSError as error:
                        if error.errno != errno.EIO:
                            raise
                        break
                    if not chunk:
                        break
                    output += chunk
                if remaining:
                    prompt, answer = remaining[0]
                    found = output.find(prompt.encode(), consumed)
                    if found >= 0:
                        os.write(fd, answer.encode() + b"\n")
                        consumed = len(output)
                        remaining.pop(0)
            else:
                self.fail("installer timed out:\n" + output.decode())
        finally:
            os.close(fd)
            # A completed shell is already a zombie; on failure this prevents leaks.
            try:
                os.kill(pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            _, status = os.waitpid(pid, 0)
        self.assertFalse(remaining, output.decode())
        return os.waitstatus_to_exitcode(status), output.decode()

    def choose(self, *answers):
        return self.run_install([("Data location [", answer) for answer in answers])

    def test_enter_keeps_linux_default_and_colors(self):
        code, output = self.choose("")
        self.assertEqual(code, 0, output)
        self.assertTrue(self.entry.is_dir())
        self.assertFalse(self.entry.is_symlink())
        self.assertIn("PAIRING pair", output)
        self.assertIn("\033[36m", output)

    def test_custom_path_with_spaces_persists_on_reinstall(self):
        target = self.root / "data with spaces"
        code, output = self.choose(str(target))
        self.assertEqual(code, 0, output)
        self.assertEqual(self.entry.resolve(), target)
        (target / "identity").write_text("keep me")
        code, output = self.run_install()
        self.assertEqual(code, 0, output)
        self.assertIn("Keeping this location", output)
        self.assertNotIn("Data location [", output)
        self.assertEqual((target / "identity").read_text(), "keep me")

    def test_pasted_default_with_trailing_slash(self):
        code, output = self.choose(str(self.entry) + "/")
        self.assertEqual(code, 0, output)
        self.assertTrue(self.entry.is_dir())
        self.assertFalse(self.entry.is_symlink())

    def test_explicit_environment_default_is_respected(self):
        target = self.root / "configured data"
        self.env["HERMESH_DATA_DIR"] = str(target)
        code, output = self.choose("")
        self.assertEqual(code, 0, output)
        self.assertTrue(target.is_dir())
        self.assertFalse(self.entry.exists())

    def test_invalid_inputs_retry_and_tilde_expands(self):
        code, output = self.choose("0", "999", "relative/path", "~/hermesh data")
        self.assertEqual(code, 0, output)
        self.assertEqual(output.count("Please enter an absolute path"), 3)
        self.assertEqual(self.entry.resolve(), self.home / "hermesh data")

    def test_nonempty_directory_is_not_adopted(self):
        target = self.root / "other installation"
        target.mkdir()
        (target / "identity").write_text("other machine")
        code, output = self.choose(str(target), "")
        self.assertEqual(code, 0, output)
        self.assertIn("folder is not empty", output)
        self.assertFalse(self.entry.is_symlink())
        self.assertEqual((target / "identity").read_text(), "other machine")

    def test_no_terminal_and_no_color(self):
        code, output = self.run_install(terminal=False)
        self.assertEqual(code, 0, output)
        self.assertIn("No terminal", output)
        self.assertNotIn("\033[", output)

    def test_no_color_environment_and_quit(self):
        self.env["NO_COLOR"] = ""
        code, output = self.choose("q")
        self.assertEqual(code, 0, output)
        self.assertNotIn("\033[", output)
        self.assertFalse(self.entry.exists())
        self.assertFalse((self.home / ".local/bin/hermeshd").exists())

    def test_unavailable_volume_does_not_create_new_data(self):
        self.entry.parent.mkdir(parents=True)
        self.entry.symlink_to(self.root / "unmounted/data")
        code, output = self.run_install(terminal=False)
        self.assertNotEqual(code, 0, output)
        self.assertIn("Mount its disk", output)
        self.assertFalse((self.root / "unmounted").exists())

    def test_low_space_can_be_corrected(self):
        self.command("df", 'case "$*" in *home*) free=500000;; *) free=90000000;; esac\n'
                     'printf "Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 100000000 0 %s 0%% /\\n" "$free"')
        code, output = self.run_install([
            ("Data location [", ""), ("Use this location anyway?", ""),
            ("Data location [", str(self.root / "larger disk/data"))])
        self.assertEqual(code, 0, output)
        self.assertIn("Less than 2 GiB", output)
        self.assertTrue(self.entry.is_symlink())

    def test_low_space_is_a_warning_for_unattended_installs(self):
        self.command("df", 'printf "Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 1000000 0 500000 0%% /\\n"')
        code, output = self.run_install(terminal=False)
        self.assertEqual(code, 0, output)
        self.assertIn("larger disk is recommended", output)
        self.assertTrue(self.entry.is_dir())

    def test_nas_only_offers_local_writable_mounted_volumes(self):
        (self.root / "VERSION").touch()
        mounts = []
        for number, filesystem, mode in [(1, "btrfs", "rw"), (2, "ext4", "rw"),
                                         (3, "nfs", "rw"), (4, "btrfs", "ro")]:
            volume = self.root / f"volume{number}"
            volume.mkdir()
            mounts.append(f"disk {volume} {filesystem} {mode} 0 0\n")
        (self.root / "mounts").write_text("".join(mounts))
        self.command("df", 'case "$*" in *volume2*) free=90000000;; *) free=3000000;; esac\n'
                     'printf "Filesystem 1024-blocks Used Available Capacity Mounted\\nfixture 100000000 0 %s 0%% /\\n" "$free"')
        code, output = self.choose("1")
        self.assertEqual(code, 0, output)
        self.assertIn("Synology storage volumes", output)
        self.assertIn(f"Default: {self.root}/volume2/@hermesh-1000", output)
        self.assertNotIn(str(self.root / "volume3"), output)
        self.assertNotIn(str(self.root / "volume4"), output)
        self.assertEqual(self.entry.resolve(), self.root / "volume1/@hermesh-1000")

    def test_cli_does_not_ask_for_daemon_data(self):
        code, output = self.run_install(args="--cli")
        self.assertEqual(code, 0, output)
        self.assertNotIn("Where should Hermesh", output)
        self.assertFalse(self.entry.exists())


if __name__ == "__main__":
    unittest.main(verbosity=2)
