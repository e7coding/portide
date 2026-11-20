# How Portide's protocol and encryption work

This document is for people who want to check Portide's security claims for themselves. It describes what the code in `core/` does, with links to the packages. Message formats are in [`proto/portide.proto`](../../proto/portide.proto). The design hasn't had an independent audit yet. To report a problem, see [SECURITY.md](../../SECURITY.md).

## Goals and threat model

Portide moves files and text between devices that belong to one person or one household, a **circle**. It aims to make sure that:

- only circle members can read what is sent, including file names and text;
- an outsider on the same network, or someone scanning the port, can't tell which devices belong to which circle or follow a device over time;
- a relay server can forward traffic but can't read it, and learns as little as possible about who talks to whom;
- a removed device (a lost phone, say) loses access to the circle.

It doesn't protect against:

- a circle member. Every member is fully trusted and can add or remove devices.
- malware on a member device.
- an attacker who has the device unlocked and its keychain open.

It also doesn't hide that a device is running Portide. The service type and port are visible on the local network.

## Building blocks

| Purpose | Algorithm | Go implementation |
| --- | --- | --- |
| Device identity, record and message signatures | Ed25519 | `crypto/ed25519` |
| Transport encryption | QUIC with TLS 1.3 | `quic-go`, `crypto/tls` |
| Pairing with a short code | CPace (PAKE) on Ristretto255 | `filippo.io/cpace` |
| Key derivation, MACs | HKDF-SHA256, HMAC-SHA256 | `crypto/hkdf`, `crypto/hmac` |
| Encrypting fields in the local database | XChaCha20-Poly1305 | `golang.org/x/crypto/chacha20poly1305` |
| Passphrase-protected key file | PBKDF2-SHA256 (600,000 iterations), then AES-256-GCM | `crypto/pbkdf2`, `crypto/aes` |
| File integrity | BLAKE3-256 | `lukechampine.com/blake3` |

Portide doesn't define any new cryptographic primitives. Every signature, MAC and hash input starts with a context string such as `portide-chain-v1\x00`. That keeps a value made for one purpose from being accepted for another.

## Keys

Each device holds three secrets in its keystore ([`core/keystore`](../../core/keystore)): the system keychain, or a passphrase-encrypted file when there is no keychain.

- **Device key.** An Ed25519 key pair created on first launch. Its **KeyID** is the SHA-256 of the public key in hex ([`core/identity`](../../core/identity)). It never leaves the device.
- **Discovery key.** A random 256-bit key that all members share. Members use it to recognize each other on the local network and to prove circle membership when they connect. It is rotated whenever a device is removed (see [Removing a device](#removing-a-device)).
- **Database key.** A random 256-bit key that encrypts sensitive fields in the local database.

## The circle: a signed membership chain

Membership is an append-only chain of Protobuf records ([`core/chain`](../../core/chain)). There are four record types:

- create circle (the genesis record);
- add device;
- remove device;
- rename device.

Each record contains the SHA-256 of the previous record. It is signed with Ed25519 by a device that was a member at that point in the chain. The **circle ID** is the SHA-256 of the genesis record.

When two members connect, they exchange chains. A device accepts the other's chain only if it validates from the genesis record and extends its own chain. If the two chains diverge at some position, the device reports a fork and doesn't merge automatically. Device names are limited to 96 bytes with no control characters, and the number of rename records is capped, so the chain stays small.

## Connections

All traffic runs over QUIC, on one UDP port that defaults to 7443 ([`core/transport`](../../core/transport)). The ALPN value tells the connection types apart:

- `portide/2`: a connection between members;
- `portide-pair/2`: a pairing connection, accepted only while a pairing code is active.

**TLS never carries a device identity.** For each handshake the server creates a new throwaway certificate, and the client presents none. Someone who scans the port completes a TLS handshake but learns no long-lived key. Both sides then prove who they are inside the encrypted channel. Each proof is bound to that connection by a TLS exporter value (RFC 5705), which a man in the middle with two separate TLS sessions can't make match.

### Member handshake

[`core/memberauth`](../../core/memberauth) runs on the first stream of a `portide/2` connection. The exporter label is `portide-member-auth-v2`.

1. **Circle proof.** Each side derives a circle key, `HKDF-Expand(SHA-256, discovery key, "portide-circle-auth-v1")`. It sends an HMAC-SHA256 of the exporter value and its own role.
   - The connecting side goes first.
   - The accepting side answers only after the proof checks out, so an outsider who connects learns nothing.
   - The roles are part of the MAC, so a proof can't be reflected back to its sender.
2. **Identity.** The connecting side sends a Hello: its public key, an Ed25519 signature over the exporter value and its role, and its chain.
   - The accepting side merges that chain and checks that the key belongs to a current member. Only then does it send its own Hello.
   - So a device that has been removed never learns the accepting side's identity.

### Local discovery

Members announce themselves over mDNS under `_portide._udp` ([`core/discovery`](../../core/discovery)).

- The instance name is a token: the first 12 bytes of `HMAC-SHA256(discovery key, 10-minute window number ‖ KeyID)`, written in hex.
- Members can match a token to a device. To outsiders the name looks random, and it changes every 10 minutes, so they can't tell which device it is or link it across windows.
- Lookups accept the window before and after the current one, to allow for clock differences between devices.

## Pairing a new device

Adding a device uses an 8-digit code shown on a device that's already a member ([`core/pairing`](../../core/pairing)).

- The code is valid for 5 minutes, and it stops working after 3 failed attempts.
- While a code is active, the inviting device announces `_portide-pair._udp` under a random instance name and accepts `portide-pair/2` connections.

The two devices run CPace with the code as the password. The CPace context includes:

- the roles (`portide-joiner` and `portide-inviter`);
- the TLS exporter value, with the label `portide-pair-v2`.

So a man in the middle ends up with different keys on each side, and pairing fails. An eavesdropper can't test guesses for the code offline.

Each side then proves it knows the shared key with a confirmation tag. The tag is an HMAC under a key derived with `HKDF-Expand(key, "portide-pair-confirm-v2")`, and it's different for each direction. Each side also signs the exporter value with its device key. After that:

- the inviting device appends an "add device" record;
- it sends the chain, the discovery key and the circle's relay settings to the new device, over the already authenticated channel.

If both devices are already in circles with other members, the two circles can be merged. This needs explicit consent (`--merge`).

## Removing a device

Any member can remove any other member, but not itself.

1. The remover creates a new discovery key.
2. It appends a "remove device" record that holds only a check value of the new key: `SHA-256("portide-discovery-key-check-v1\x00" ‖ key)`. The key itself isn't in the record.
3. It sends the new key to the remaining members over member connections only.
4. A member accepts a new key only if it matches the check value in the latest removal record, after merging the chain.

The removed device can read the removal record, but it can't work out the new key. After the key changes, the removed device can't recognize members' discovery tokens or pass the circle proof.

Members that were offline during the removal still have the old key. For a limited time, a device keeps accepting the old key on incoming connections, so those members can connect, pass the identity check and receive the new key. The removed device can use the old key in that window too, but the identity step rejects it before the accepting side reveals anything.

**Known limitation.** The connecting side sends its identity first, so a device that connects to a removed device can reveal its own identity to it. Rotating the discovery key limits this: once a member has the new key, it no longer recognizes or dials the removed device. The relay token also needs replacing; see the [relay guide](relay.md#tokens).

## Sending files and text

Transfers run on streams of an authenticated member connection ([`core/transfer`](../../core/transfer)).

1. The sender sends an **offer** listing the items: file names, relative paths, sizes, and the text for short messages. The receiver accepts automatically or asks the user, depending on its receive settings.
2. Each file is streamed, then checked against a BLAKE3-256 hash. Interrupted transfers resume from where they stopped.
3. The receiver cleans every path before writing:
   - it rejects absolute paths, drive letters and `..`;
   - it replaces illegal and control characters, and renames Windows reserved names such as `CON`;
   - it writes through Go's `os.Root`, so a symlink can't lead outside the save folder;
   - offers have limits on item count, path length and message size.

When a device is offline, the sender keeps the item queued and delivers it when the device comes back.

## Relays

A relay ([`core/relay`](../../core/relay), server in [`core/relayserver`](../../core/relayserver)) is not a circle member. It only forwards encrypted QUIC packets between members. The member connection inside is the same `portide/2` connection, with the same handshake, as on a local network.

- **Who can use a relay.** Devices verify the relay by the SHA-256 fingerprint of its Ed25519 public key. That fingerprint is part of the config string (`portide-relay://host:port?fp=…&token=…`), so no certificate authority or domain is involved. The relay admits a device only with a valid access token.
- **Registration.** A device registers under a **routing ID**, `SHA-256("portide-route-v1\x00" ‖ device public key)`.
  - The routing ID isn't the KeyID. Only someone who already knows the device's public key, in practice another member, can compute it.
  - The device signs the exporter value of its relay connection (label `portide-relay-register-v1`) with its device key, so a registration can't be replayed on another connection.
- **What the relay sees:**
  - public IP addresses;
  - routing IDs and device public keys (it checks the registration signature with the key);
  - which routing IDs open sessions to each other;
  - connection times and byte counts.
- **What it can't see:** file contents, file names, text, device names or the membership list. It doesn't store anything it forwards.
- **Direct connections.** The relay tells each device the public address it sees for it. Devices exchange those addresses inside their encrypted connection and try to connect directly (hole punching). The relay doesn't take part in that exchange.
- **Shared relay list.** The circle's relay list, up to 4 relays, is signed by the member that last changed it ([`core/relayset`](../../core/relayset)). The receiving device checks that the signer is a current member and keeps the newest version.

## Data at rest

The local database is SQLite ([`core/store`](../../core/store)).

- **Encrypted fields:** message text, file names and paths, with XChaCha20-Poly1305 under the database key.
  - Each value gets a random 24-byte nonce.
  - The field name is the associated data, so a ciphertext can't be moved into a different field.
- **Plaintext fields:** timestamps, sizes and states, which the app uses for sorting and queries.
- **Received files** are ordinary files in the save folder, not encrypted by Portide.

## Update checks

Release builds of the desktop app and the `portide` command line check GitHub once a day for a newer release ([`core/update`](../../core/update)).

- **What GitHub sees:** the device's IP address and a `Portide/<version>` User-Agent. Nothing else is sent.
- **What happens next:** the app only shows a notice. It doesn't download or install anything.
- **Turning it off:**
  - in the desktop app, under Settings › About;
  - for the command line, set `PORTIDE_NO_UPDATE_CHECK=1`.
- Builds compiled from source don't check at all.

## Not covered yet

- **Forward secrecy for stored data.** Someone who gets the database key can read the stored history.
- **Membership is all-or-nothing.** There are no roles or per-device permissions.
- **Metadata on the local network.** An observer can see that a device runs Portide, and roughly when it transfers data.
- **No independent audit.** If you review the code, reports are welcome (see [SECURITY.md](../../SECURITY.md)).
