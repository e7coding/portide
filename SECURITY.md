# Security policy

Portide's whole point is that only your own devices can read what you send, so security reports get priority over everything else.

## Reporting a vulnerability

**Please don't open a public issue.** Report privately through GitHub instead: on this repository's **Security** tab, choose **Report a vulnerability**. Only the maintainers can see the report.

Useful things to include:

- what an attacker can do (read content, impersonate or add a device, crash a client, learn something the relay shouldn't know, and so on);
- what they need first (same local network, control of a relay, a stolen device, a pairing code they watched being typed);
- steps or code to reproduce it, and the versions and platforms you tested.

We'll acknowledge the report within 3 working days and keep you updated until it's fixed. Once a fix ships we'll publish an advisory and credit you, unless you'd rather stay anonymous. Please give us up to 90 days before disclosing publicly; if we need longer, we'll explain why.

## What's in scope

- the protocol and its implementation: pairing, circle membership (the signed chain), member handshakes, transfers and the relay protocol (`core/`);
- the desktop app, the iOS app and its share extension, and the `portide` command line;
- the relay server (`portide-relay`), in particular anything that lets a relay or anyone on the path read content, file names or circle membership, or link devices beyond what is documented;
- how keys and history are stored on disk and in the system keychain.

What the relay is expected to see (public IP addresses, routing IDs, device public keys, connection times and byte counts) is documented in [docs/中继部署.md](docs/中继部署.md). A way to learn more than that is in scope.

Out of scope: attacks that need an already unlocked device with Portide open, denial of service by flooding a relay you don't operate, and reports from automated scanners with no demonstrated impact.

## Supported versions

Portide hasn't had a stable release yet. Fixes go into the latest release only; please test against it or against `main`.
