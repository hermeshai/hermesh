#!/usr/bin/env bash
# Run from the repository root. No host account state enters the test container.
set -euo pipefail
arch=${1:-arm64}
fixture=$(mktemp -d)
trap 'rm -rf "$fixture"' EXIT
chmod 755 "$fixture"
python3 scripts/build-provider-fixture.py "$fixture" --arch "$arch"
docker run --rm --init --user node --cap-drop=ALL --security-opt=no-new-privileges \
  --platform "linux/$arch" -v "$fixture:/fixture:ro" \
  -e HERMESH_PROVIDER_ACCEPTANCE=1 -e HERMESH_PROVIDER_FIXTURE_BIN=/fixture \
  -e HERMESH_PROVIDER_ACCOUNT_TESTS -e HERMESH_PROVIDER_PREVIOUS=/fixture/previous \
  -i node:24-bookworm bash -se <<'CONTAINER'
set -euo pipefail
cd "$HOME"
mkdir -p .local/bin
export PATH="$HOME/.local/bin:$PATH"
npm install --global --prefix "$HOME/.local" --no-audit --no-fund \
  @google/gemini-cli@0.71.1 @augmentcode/auggie@1.2.3 @deepseek-ai/dsh@0.2.2-rc.5 \
  @moonshot-ai/kimi-code@2.8.1 @qwen-code/qwen-code@0.28.7 \
  @tencent-ai/codebuddy-code@2.187.0 @xai-official/grok@1.0.92
install_vendor() {
  local url=$1
  shift
  curl --fail --silent --show-error --location --retry 2 "$url" -o /tmp/vendor-install
  timeout 5m bash /tmp/vendor-install "$@"
}
install_vendor https://antigravity.google/cli/install.sh
install_vendor https://cursor.com/install
install_vendor https://cli.kiro.dev/install --version 3.6.2
CONFIGURE=false GOOSE_VERSION=v1.61.1 install_vendor https://github.com/aaif-goose/goose/releases/download/v1.61.1/download_cli.sh
install_vendor https://qoder.com/install --version 1.1.100
exec /fixture/test -test.v -test.run '^TestProviderMaintenanceReal$' -test.parallel 3 -test.timeout 45m
CONTAINER
