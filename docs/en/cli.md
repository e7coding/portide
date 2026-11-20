# Portide command line (`portide`)

`portide` is the command-line client for Portide. It speaks the same protocol as the desktop and iOS apps, so a server or a headless machine can join the same circle as your laptop and phone.

## Get started

Unpack the archive for your platform and put `portide` (or `portide.exe`) somewhere on your `PATH`.

```bash
portide init --name "home server"   # create this device's identity
portide daemon                      # stay online: receive, discover devices, pair
```

Keep `portide daemon` running (for example as a systemd service). The other commands talk to it.

To join a circle, show a code on a device that's already in it (**Add device › Show code** in the apps, or `portide pair`), then on this machine:

```bash
portide join 12345678                       # finds the other device on the local network
portide join 12345678 --addr 192.168.1.5:7443   # if discovery doesn't work (different subnet)
```

If both sides already have other devices, the two circles merge, which needs `--merge`.

## Commands

| Command | What it does |
| --- | --- |
| `init --name NAME` | Create this device's identity and set its name |
| `whoami` | Show this device |
| `daemon [--listen ADDR] [--peer HOST:PORT]… [--no-mdns]` | Run the daemon (default port 7443) |
| `pair` | Show an 8-digit code for adding a new device (needs the daemon) |
| `join CODE [--addr HOST:PORT] [--merge]` | Join a circle with a code |
| `devices` | List the devices in the circle and whether they're online |
| `devices rm DEVICE [--yes]` | Remove a device (for example a lost phone) and replace the circle's keys |
| `devices rename DEVICE NAME` | Rename a device (this one or another); syncs to the whole circle |
| `send DEVICE[,DEVICE…] [--text TEXT] [FILE_OR_FOLDER…]` | Send text, files or folders. Offline devices get it when they come online |
| `queue` | Show queued, running and recently finished sends |
| `queue cancel ID` / `queue pause ID` / `queue resume ID` / `queue retry ID` | Manage a send |
| `history [-n COUNT] [--device DEVICE]` | Show what was sent and received (newest first) |
| `history show ID` / `history rm ID` / `history clear [--yes]` | Show, delete or clear history |
| `inbox` | List incoming items waiting for your OK |
| `accept ID` / `reject ID` | Accept or reject one |
| `config` | Show the save folder, receive policy and retention |
| `config receive auto\|ask\|SIZE` | Accept automatically, always ask, or ask above a size such as `100MB` |
| `config save-dir DIR` | Where received files go (default: Downloads/Portide) |
| `config queue-days DAYS` | How long queued sends wait for an offline device (default 7) |
| `config history-days DAYS` | How long history is kept (default 0, forever) |
| `relay` | List relays and their connection status |
| `relay add CONFIG` | Add or update a relay (up to 4). `portide relay add -` reads the config string from standard input, so the token stays out of your shell history |
| `relay rm [NUMBER\|ADDRESS]` | Remove a relay |
| `version` | Show the version (also `--version`). Release builds also say on standard error when a newer release is out |

`DEVICE` can be a device's name or the start of its key ID, as shown by `portide devices`.

## Environment

| Variable | Meaning |
| --- | --- |
| `PORTIDE_HOME` | Data folder (default: `portide` in the system config folder, the same one the desktop app uses). Also `--data DIR` |
| `PORTIDE_KEYSTORE` | Where keys are kept: `auto` (default), `os` (system keychain) or `file` (encrypted file) |
| `PORTIDE_PASSPHRASE` | Passphrase for the `file` keystore |
| `PORTIDE_LANG` | Output language, `en` or `zh`. By default `portide` follows `LANG` / `LC_ALL` and uses English when neither is set |
| `PORTIDE_NO_UPDATE_CHECK` | Set to `1` to stop checking GitHub for a newer release. Otherwise release builds check at most once a day, when you run `portide version` and while `portide daemon` runs (it logs one line when a newer release appears). GitHub sees your IP address; nothing else is sent |

Don't run `portide daemon` while the desktop app is running on the same data folder; the other commands work alongside it.

For relays, see the relay guide that ships with `portide-relay`, or `docs/en/relay.md` in the source repository.
