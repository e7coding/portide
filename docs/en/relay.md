# Running a Portide relay (`portide-relay`)

A relay lets your devices reach each other when they aren't on the same network. Each online device keeps one connection to the relay; when two devices can't connect directly, the relay forwards their traffic.

The relay only moves encrypted data. It sees public IP addresses, routing IDs (hashes of device public keys), the device public keys themselves (it checks a signature with them when a device registers), connection times and byte counts. It can't see files, file names, text, device names or who is in a circle, and it stores nothing it forwards.

The relay also tells each device the public address it sees for it (usually the address after NAT). The device passes that to the other device over their already encrypted connection, and they try to connect directly (hole punching). The relay takes no part in that and doesn't see the addresses the devices exchange.

`portide-relay` prints and logs in English unless the system language is Chinese; set `PORTIDE_LANG=en` or `PORTIDE_LANG=zh` to choose. 中文说明见 `中继部署.md`。

## What you need

A server with a public IP address (any VPS or cloud instance), with **UDP 7443** and **TCP 7443** open. TCP is the fallback when UDP is blocked. Open them in both places:

- the server's own firewall, for example `sudo ufw allow 7443/udp` and `sudo ufw allow 7443/tcp`;
- your cloud provider's security group or firewall rules: inbound from 0.0.0.0/0 on port 7443, one rule for UDP and one for TCP. Many providers allow only TCP by default, so the UDP rule is the one most often missed.

If neither gets through, devices show a "relay didn't respond" error. To check, run `sudo tcpdump -ni any port 7443` on the server and try connecting again: if no packets show up, the cloud firewall is dropping them.

## Option 1: run the binary

```bash
tar xzf portide-relay-<version>-linux-amd64.tar.gz && cd portide-relay-<version>-linux-amd64
./portide-relay serve --public-addr YOUR_SERVER_IP_OR_DOMAIN:7443
```

On first run it creates the relay's key and a first access token, and prints a line like:

```
portide relay add portide-relay://YOUR_SERVER_IP:7443?fp=…&token=…
```

That's the config string. Add it on any one device: paste it under **Settings › Network & relays › Add relay** in the apps, or run the printed command with the `portide` command line. It syncs to every device in the circle. The string is printed only this once, so keep it safe; if you lose it, create another with `portide-relay token add`.

The first-run "default" token ends up in your service logs. Once you've created your own named token and set up your devices with it, revoke the default one.

Keys and tokens are stored in `~/.config/portide-relay` by default; change it with `--data` or the `PORTIDE_RELAY_DATA` environment variable.

### As a systemd service

Create a dedicated system user:

```bash
sudo useradd --system --home /var/lib/portide-relay --shell /usr/sbin/nologin portide-relay
```

Then `/etc/systemd/system/portide-relay.service`:

```ini
[Unit]
Description=Portide relay
After=network-online.target

[Service]
ExecStart=/opt/portide-relay/portide-relay --data /var/lib/portide-relay serve --public-addr YOUR_SERVER_IP:7443
Restart=on-failure
User=portide-relay
StateDirectory=portide-relay

[Install]
WantedBy=multi-user.target
```

Run `sudo systemctl enable --now portide-relay` and find the first config string with `journalctl -u portide-relay`.

Manage tokens as the same user, for example `sudo -u portide-relay /opt/portide-relay/portide-relay --data /var/lib/portide-relay token add …`. Don't run token commands as root: the service can't read the files root creates, and new devices fail to register (the relay log says it can't read the token file).

## Option 2: Docker

In the unpacked folder (it contains `portide-relay` and a `Dockerfile`):

```bash
docker build -t portide/relay .
docker run -d --name portide-relay --restart unless-stopped \
  -p 7443:7443/udp -p 7443:7443/tcp -v portide-relay:/data \
  -e PORTIDE_RELAY_ADDR=YOUR_SERVER_IP:7443 portide/relay
docker logs portide-relay        # shows the first config string
```

Manage tokens inside the container: `docker exec portide-relay /portide-relay token list`.

## When UDP is blocked: the TCP fallback

The relay listens on UDP 7443 and TCP 7443 by default (`--tcp-listen` defaults to the same address as `--listen`; `--tcp-listen ""` turns TCP off). Devices try UDP first; if that hasn't connected within 3 seconds they also try TCP and use whichever connects first. Office networks, hotel Wi-Fi and public hotspots often block UDP, and some cloud firewalls allow only TCP; devices handle this on their own.

- Over TCP the relay status reads "Connected (TCP)".
- TCP is slower: QUIC runs inside TCP, so lost packets wait for retransmission, and large files through a TCP relay are noticeably slower.
- Devices probe UDP every 10 minutes and switch back when it works again.
- A TCP connection's source address can't be used for hole punching, so it isn't passed on as the device's public address. Devices still try local addresses, global IPv6 addresses and the other side's public address (if that side is on UDP), and stay on the relay only if none work.
- If the public TCP port differs from the UDP port (for example 443, below), put it in the config string with `--tcp-port`: `portide-relay serve --tcp-port 443` (for the first-run string) or `portide-relay token add --tcp-port 443`. The string then contains `tcp=443`. Without it, devices use the same port as UDP, which is also right when Docker maps 7443 to another port (`-p 17443:7443/udp -p 17443:7443/tcp`).

## Sharing port 443 with a website

Strict networks often allow only port 443. If a website already uses 443 on your server, nginx's `stream` module can split traffic by ALPN: connections with ALPN `portide-relay-tcp/1` go to the relay's TCP port, everything else to the website. nginx needs the `stream` and `ssl_preread` modules (`nginx -V` lists `--with-stream` and `--with-stream_ssl_preread_module`). On Ubuntu/Debian `stream` is a dynamic module: install `libnginx-mod-stream` first.

At the top level of `nginx.conf` (next to `http {}`):

```nginx
stream {
    map $ssl_preread_alpn_protocols $upstream {
        ~\bportide-relay-tcp/1\b  portide_relay;
        default                  website;
    }

    upstream portide_relay { server 127.0.0.1:7443; }
    upstream website     { server 127.0.0.1:8443; }

    server {
        listen 443;
        ssl_preread on;
        proxy_pass $upstream;
    }
}
```

Move the website's own `listen 443 ssl;` to a local port inside `http {}`, or nginx won't start because 443 is taken:

```nginx
http {
    server {
        listen 127.0.0.1:8443 ssl;
        server_name your.domain;
        # certificates and the rest unchanged
    }
}
```

Then:

1. keep the relay on 7443 (TCP is now only used by nginx locally, so you can use `--tcp-listen 127.0.0.1:7443` and stop exposing TCP 7443);
2. create config strings with `--tcp-port 443`: `portide-relay token add --addr your.domain:7443 --tcp-port 443`;
3. open TCP 443 in the firewall and security group (UDP stays on 7443);
4. `sudo nginx -t && sudo systemctl reload nginx`.

Behind `stream`, every connection reaches the relay from 127.0.0.1, so IP addresses in `--debug` logs aren't meaningful.

## Tokens

- `portide-relay token add --name home --addr YOUR_SERVER_IP:7443` creates a token and prints its config string (add `--tcp-port` if the public TCP port differs).
- `portide-relay token list` lists tokens by number, name and creation time. The tokens themselves aren't stored.
- `portide-relay token revoke NUMBER` revokes a token; devices using it are disconnected within seconds.

Tokens decide who may use the relay. One token per circle is enough: when you pair a new device, it receives the circle's relay settings automatically.

**If you lose a device**, in this order:

1. Remove it from the circle on another device (Devices › the device › Remove from circle, or `portide devices rm`).
2. On the relay, create a new token with `portide-relay token add`, then revoke the old one with `portide-relay token revoke`.
3. Update the relay on any device with the new config string (adding the same relay again replaces its token). It syncs to the rest of the circle; devices on older versions don't receive the change and need it set by hand, and the apps tell you which ones.

Until the token is replaced, the removed device can still register with the relay: it can tell whether members are online, use up the token's session slots, and reach devices that haven't yet learned it was removed.

## Several relays

A circle can use up to 4 relays. Devices connect to all of them at once, not as primary and backup, so if one is down the others keep working. The relay list is stored in the circle and syncs to every device within seconds of a change on any one of them.

## Limits

- `--max-clients N`: how many devices may be registered at once (default 1000).
- `--max-sessions N`: concurrent sessions per token (default 64).
- `--session-rate 10MB`: bytes per second per session per direction (default unlimited).

## UDP buffers

If the startup log says `failed to sufficiently increase receive buffer size`, the system's UDP buffers are too small and large transfers through the relay are slower (connections still work). To raise them (root needed; restart the relay afterwards):

```bash
echo -e 'net.core.rmem_max=7500000\nnet.core.wmem_max=7500000' | sudo tee /etc/sysctl.d/90-portide-relay.conf
sudo sysctl --system
```

With Docker, run this on the host.

## Logs

By default the relay logs only aggregate counts every 10 minutes (online devices, sessions, bytes forwarded), with no IP addresses or routing IDs. For troubleshooting, `--debug` logs each registration and session, including IP addresses and routing ID prefixes.
