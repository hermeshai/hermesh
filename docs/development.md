# Development

```bash
make generate   # Go, Swift and TypeScript code from proto
make lint test  # static checks and unit tests
make test-app   # the desktop app's types and unit tests, and the signer's
make e2e        # full user flows and malicious-relay tests with real binaries and scripted agents
make e2e-real   # the same flows with the real Claude Code and Codex on this machine (uses quota)
HERMESH_DOCKER=1 go test ./e2e -run Linux   # a Linux machine (Docker)
HERMESH_IPHONE_E2E=1 go test ./e2e -run TestIPhone -timeout 60m   # the iPhone app in the Simulator
make app        # build and sign dist/Hermesh.app (see below for a release build)
pnpm --dir apps/desktop dev   # the desktop app against the local hermeshd (HERMESH_SOCKET to choose one)
```

`make e2e` includes the desktop app's tests, which open the app on this Mac;
`go test ./e2e -skip 'TestDesktop$|TestSignerApp$'` leaves them out.

## Releases

A release build of the desktop app needs, besides the Developer ID certificate in the keychain, the signer's Developer ID provisioning profile (App ID `com.hermesh.app.signer`, so the root key stays in the signer's Keychain group) and an App Store Connect API key to notarize:

```bash
HERMESH_SIGNER_PROFILE=~/.config/hermesh/signer.provisionprofile \
HERMESH_NOTARY_KEY_PATH=~/.appstoreconnect/private_keys/AuthKey_<key id>.p8 \
HERMESH_NOTARY_KEY_ID=<key id> HERMESH_NOTARY_ISSUER=<issuer id> \
make app        # HERMESH_ARCH=arm64 for an Apple silicon build only
```

The release workflow reads the same from the secrets `MACOS_SIGNER_PROFILE`, `APPLE_API_KEY_P8`, `APPLE_API_KEY_ID` and `APPLE_API_ISSUER_ID`.

Releasing: every part of Hermesh has one version, and each version is one GitHub release holding every platform's files (the programs, the Mac app, the Windows app): every updater and the install command read `releases/latest/download/…`, which names one release. The iPhone app is not part of these releases: the same version goes to TestFlight from a Mac (`apps/ios/scripts/testflight.sh`, [iphone-app.md](iphone-app.md) §6).

1. **Notes.** Before tagging, the version's highlights go in `apps/desktop/src/locales/{en,zh-CN}/releases.json` (rename `unreleased` to the version, and leave an empty `unreleased`); without them the release workflow stops. Each line says where it applies when that is not everywhere ("On the iPhone…", "On Linux…"). The release's text starts with which file to install for what (`install` in the same folder's `updates.json`), then the highlights, then a link to every change since the last release.
2. **Tag.** Pushing a `v*` tag runs `release.yml`. It makes a draft release, the builds upload their files to it, and `scripts/release-check.sh` checks them all (every program in the signed checksum list, the Windows programs, the version, install.sh's and install.ps1's key, the Mac app's feeds naming this version). Only then is the draft published, as a **prerelease**: nothing installed updates to it, and the latest release stays as it was. The relay image of the version is on ghcr.io only. A failed run leaves a draft; fix it and run the workflow again, or delete the draft and tag a new version.
3. **Try it.** On a machine: `curl -fsSL https://github.com/hermeshai/hermesh/releases/download/<tag>/install.sh | HERMESH_VERSION=<tag> sh` (on a paired machine it updates it). On a Mac: the prerelease's DMG. Your own server: `ghcr.io/hermeshai/hermesh-relay:<tag>`.
4. **Promote.** `gh workflow run promote.yml -f tag=<tag>` checks the files again, copies the relay image to Docker Hub and makes it latest, then makes the release the latest. Within hours every machine and Mac app updates to it; the workflow waits until `releases/latest` serves it.

The first time, run `scripts/release-keygen.sh` to generate the release signing key (the private key goes in the GitHub secret `RELEASE_SIGNING_KEY`; commit the public key). Each release carries `Hermesh-macos-<arch>.dmg` for installation, and `Hermesh-macos-<arch>.zip` with `RELEASES-darwin-<arch>.json` for installed apps to update themselves (`arm64` and `x64`; [docs/desktop-app.md](desktop-app.md) §6.5–6.7). The app and DMG are Developer ID–signed, notarized and stapled. For Windows it carries `Hermesh-windows-<arch>-setup.exe` (unsigned) for installation, and `Hermesh-windows-<arch>.nupkg` with `RELEASES-win32-<arch>.json` and its `.sig`, signed with the release key, for installed apps to update themselves ([docs/windows.md](windows.md) §6.1–6.2). A whole update, with two signed builds made here and served locally, is checked by `HERMESH_UPDATE_E2E=1 go test ./e2e -run TestDesktopUpdate -v -timeout 30m`. The binaries' signed `checksums.txt` also lists `version`, which installed machines read to update themselves ([machine-updates.md](machine-updates.md)); `HERMESH_DOCKER=1 go test ./e2e -run TestLinuxUpdate -v` checks that on a Linux server in Docker.
