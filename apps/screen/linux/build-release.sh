#!/bin/sh
# Builds hermesh-screen for Linux as a release carries it, in AlmaLinux 8
# (glibc 2.28: Ubuntu 20.04, Debian 10, RHEL 8, Fedora and later run it),
# for this machine's architecture. It is built against glibc, not musl like
# hermeshd and her, because it loads PipeWire at run time, which a static
# program can't; C++ is linked in (OpenH264), so it needs nothing else.
#
#   apps/screen/linux/build-release.sh <out file>
set -eu
cd "$(dirname "$0")"
out="$1"
toolchain="$(sed -n 's/^channel = "\(.*\)"/\1/p' rust-toolchain.toml)"
mkdir -p "$(dirname "$out")"
docker run --rm -v "$PWD:/src:ro" -v "$(cd "$(dirname "$out")" && pwd):/out" \
  -v hermesh-screen-release-cargo:/root/.cargo -v hermesh-screen-release-target:/target \
  -e CARGO_TARGET_DIR=/target -e TOOLCHAIN="$toolchain" -e NAME="$(basename "$out")" \
  almalinux:8 sh -c '
set -e
dnf install -y -q dnf-plugins-core >/dev/null && dnf config-manager --set-enabled powertools
dnf install -y -q gcc gcc-c++ make nasm libstdc++-static binutils >/dev/null
[ -x /root/.cargo/bin/cargo ] || curl -fsSL https://sh.rustup.rs | sh -s -- -y -q --profile minimal --default-toolchain none
export PATH=/root/.cargo/bin:$PATH
rustup toolchain install --profile minimal "$TOOLCHAIN" >/dev/null 2>&1
cd /src
# libstdc++ inside the program: CXXSTDLIB empty keeps the build from linking the shared one.
CXXSTDLIB= RUSTFLAGS="-C link-arg=-l:libstdc++.a" cargo +"$TOOLCHAIN" build -q --release --locked
cp /target/release/hermesh-screen "/out/$NAME"
echo "needs: $(objdump -T "/out/$NAME" | grep -o "GLIBC_[0-9.]*" | sort -uV | tail -1), $(objdump -p "/out/$NAME" | grep NEEDED | tr -s " " | cut -d" " -f3 | tr "\n" " ")"
'
"$out" --version 2>/dev/null || true
