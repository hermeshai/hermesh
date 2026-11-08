"""Cross-build a static Linux amd64 CLI with the pinned Zig toolchain."""
import os
from pathlib import Path
import shutil
import subprocess
import ziglang

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
DEST = HERE / '.state/bin'
env = dict(os.environ)
env['PATH'] = str(Path(ziglang.__file__).parent) + os.pathsep + str(Path.home()/'.cargo/bin') + os.pathsep + env['PATH']
subprocess.run(['rustup', 'target', 'add', 'x86_64-unknown-linux-musl'], cwd=REPO, env=env, check=True)
subprocess.run(['cargo-zigbuild', 'build', '--locked', '--release', '--target', 'x86_64-unknown-linux-musl', '-p', 'her-cli', '--bin', 'her'], cwd=REPO, env=env, check=True)
DEST.mkdir(parents=True, exist_ok=True)
shutil.copy2(REPO/'target/x86_64-unknown-linux-musl/release/her', DEST/'her')
(DEST/'revision.txt').write_bytes(subprocess.check_output(['git','rev-parse','HEAD'], cwd=REPO))
(DEST/'source.patch').write_bytes(subprocess.check_output(['git','diff','--binary','--','crates','Cargo.toml','Cargo.lock','rust-toolchain.toml'], cwd=REPO))
print(DEST/'her')
