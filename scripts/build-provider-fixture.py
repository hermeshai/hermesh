"""Build the two daemon releases and clients for TestProviderMaintenanceReal."""
import argparse
import json
import os
from pathlib import Path
import subprocess

parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("out", type=Path)
parser.add_argument("--os", choices=["linux", "darwin"], default="linux")
parser.add_argument("--arch", choices=["arm64", "amd64"], default="arm64")
args = parser.parse_args()
out = args.out.resolve()
out.mkdir(parents=True, exist_ok=True)
env = dict(os.environ, GOOS=args.os, GOARCH=args.arch, CGO_ENABLED="0")


def go(*argv):
    subprocess.run(["go", *argv], env=env, check=True)


go("build", "-o", str(out) + "/", "./cmd/hermeshd", "./cmd/hermesh-relay", "./cmd/hermesh",
   "./internal/runtime/acp/fakeacp", "./bench/fakecodex")
go("test", "-c", "-o", str(out / "test"), "./e2e")
source = Path("internal/machine/provider_maintenance.go").resolve()
old = source.read_text()
for new, previous in [("2.1.332", "2.1.331"), ("0.172.0", "0.171.0"),
                      ("1.0.117", "1.0.116"), ("2.4.2", "2.4.1")]:
    if old.count(f'"{new}"') != 1:
        raise ValueError(f"Update the fixture version pair for {new}")
    old = old.replace(f'"{new}"', f'"{previous}"')
(out / "previous.go").write_text(old)
overlay = out / "overlay.json"
overlay.write_text(json.dumps({"Replace": {str(source): str(out / "previous.go")}}))
go("build", "-overlay=" + str(overlay), "-o", str(out / "previous"), "./cmd/hermeshd")
