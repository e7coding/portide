# Portide

Send files and text between your own devices: phone to laptop, laptop to desktop, at home or across the internet. End-to-end encrypted, with no account and no cloud storage.

[中文说明](README.zh-CN.md)

> **Status: 0.5.2.** Windows, macOS and Linux downloads are on the [releases page](https://github.com/e7coding/portide/releases/latest). The Mac app is signed and notarized by Apple; the Windows installer isn't code-signed yet. There is no official relay yet, so connecting across networks needs a relay you run yourself. An iOS app will come later, and Android is on the roadmap.

## What it does

- **Your devices form a private circle.** Pair two devices once with an 8-digit code. After that they recognize each other automatically. There are no accounts and no sign-ups.
- **Direct on your network.** Devices on the same network find each other and connect directly. Nothing goes through a server, so speed depends only on your network.
- **Across networks, with a relay you can run yourself.** When devices aren't on the same network, they connect through a relay and try to switch to a direct connection (hole punching). The relay only forwards encrypted data. It can't read files, file names, text, device names, or who is in your circle.
- **Sends even when the other device is offline.** Sends wait in a queue and go out as soon as the other device comes online. Large transfers can be paused and resume where they stopped.
- **One conversation per device.** History, search, and an "All devices" timeline. Received files stay on disk like any other file.
- **Desktop and command line work together.** The desktop app and the `portide` CLI speak the same protocol and can be in the same circle.
- **English and Chinese interface.**

## Platforms

| Platform | Status | Guide |
| --- | --- | --- |
| Windows 10 / 11 (x64) | Installer and portable zip (not yet code-signed) | [Desktop guide](docs/en/desktop.md) |
| macOS 11+ (Apple silicon, Intel) | DMG, signed and notarized | [Desktop guide](docs/en/desktop.md) |
| Linux (x86-64) | `.deb` and tar.gz | [Desktop guide](docs/en/desktop.md) |
| iOS / iPadOS | Later | |
| Command line (`portide`) | Linux, macOS, Windows | [CLI guide](docs/en/cli.md) |
| Relay server (`portide-relay`) | Linux binary or Docker | [Relay guide](docs/en/relay.md) |
| Android | Planned | |

Privacy policy and terms of use (drafts): [docs/legal/](docs/legal/). Chinese versions of the guides are in [docs/](docs/).

## How the security works

- Each device has its own Ed25519 identity key, created on first launch and kept in the system keychain (or encrypted with a passphrase when there is none).
- Circle membership is an append-only chain of records signed by member devices: create, join, remove, rename. Removing a device replaces the circle's keys.
- Pairing uses CPace, a password-authenticated key exchange, bound to the TLS session. A wrong code or a man in the middle makes pairing fail instead of leaking anything.
- All connections use QUIC (TLS 1.3) with throwaway certificates, so scanning the port reveals no long-term identity. Devices prove circle membership and identity inside the encrypted channel, bound to that connection.
- Message text, file names and paths are encrypted with XChaCha20-Poly1305 in the local database.
- The relay sees public IP addresses, routing IDs (hashes of device public keys), device public keys, connection times and byte counts. It stores nothing it forwards.

The full write-up, including the threat model and known limitations, is in [docs/en/protocol.md](docs/en/protocol.md). To report a vulnerability, see [SECURITY.md](SECURITY.md).

## Repository layout

- `core/`: protocol, storage and transfers, shared by every client
- `app/`: the service layer the desktop and iOS interfaces call
- `cmd/portide`: command-line client
- `cmd/portide-relay`, `core/relayserver`, `deploy/relay`: relay server
- `desktop/`: desktop app (Wails v3 and React), its own Go module
- `mobile/`: Go entry point for the mobile apps, called through gomobile; its own Go module. The iOS app itself is not open source.
- `scripts/`: build scripts (`build-desktop.sh`, `package.sh`)
- `spikes/`: throwaway prototypes from the early technical checks

Building needs Go 1.26. The desktop app also needs Node.js 22 and pnpm. See the platform docs above for details.

## CI and releases

`.github/workflows/ci.yml` runs on every push to `main` and every pull request: Go tests for all three modules, the desktop UI tests, and a Linux and Windows packaging check. To release, set the version in `core/version/VERSION` and push a matching tag (`v0.5.0`): `.github/workflows/release.yml` builds every package (including Intel and Apple silicon dmgs), drafts a GitHub release with checksums, and pushes the relay image to `ghcr.io/<owner>/portide-relay`. When the repository has the signing secrets listed at the top of `release.yml`, the dmgs are signed and notarized by Apple; the Windows installer isn't code-signed yet. `.gitlab-ci.yml` runs the same tests and Linux and Windows packaging on GitLab, without macOS.

## License

- Everything is licensed under [Apache-2.0](LICENSE), except:
- the relay server (`core/relayserver/`, `cmd/portide-relay/`, `deploy/relay/`), which is licensed under [AGPL-3.0](core/relayserver/LICENSE). Client code must not import `core/relayserver`; protocol pieces both sides need live in `core/relay` (Apache-2.0).

Portide draws on the public design ideas of [LocalSend](https://github.com/localsend/localsend) (Apache-2.0) but does not copy any of its code, and the protocol was designed independently.

The Portide name and icon are not covered by the licenses above. Before contributing, read [CONTRIBUTING.md](CONTRIBUTING.md) and sign the [CLA](CLA.md).
