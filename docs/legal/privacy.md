<!--
草稿，发布前要做的：
1. 把 [YOUR LEGAL NAME] 换成你的法定姓名（个人开发者账号在 App Store 上显示的卖家名就是它）。
2. 定下网址后放到公开可访问的地方（官网，或公开后的 GitHub 仓库里这个文件），填进 App Store Connect 的"隐私政策网址"。
3. 官方中继上线前核对第 4 节：服务器所在地区、云服务商、日志保留天数和这里写的一致。
4. 最好请懂 GDPR / 加州隐私法的人看一遍。
改动后同时更新文末的日期。
-->

# Portide Privacy Policy

Effective: November 19, 2026

Portide is an app for sending files and text between your own devices. It is published by [YOUR LEGAL NAME], an individual developer ("we", "us"). This policy explains what information is involved when you use Portide and what happens to it.

The short version: **we don't have accounts, we don't run analytics, and we can't see what you send.** Your files and messages go from one of your devices to another, end-to-end encrypted.

## 1. What stays on your devices

Everything you send and receive (files, photos, text, file names, and your transfer history) is stored only on your own devices. Device names, the list of devices in your circle, and your settings are also stored only on your devices and synced among them, encrypted.

Message text, file names and paths are encrypted in the app's local database, and the keys are kept in your system keychain. Received files are saved as ordinary files in the folder you choose (or your photo library, if you allow it on iPhone and iPad).

We never receive any of this. You can delete your history in the app at any time, and uninstalling Portide removes its data from that device.

## 2. Information we don't collect

The Portide apps (iOS, iPadOS, macOS, Windows, Linux) and the `portide` command line:

- don't ask for your name, email address or phone number, and have no sign-in;
- don't include analytics, advertising or crash-reporting code;
- don't track you across apps or websites;
- don't send us your files, messages, contacts, location or any device identifiers.

## 3. Connections between your devices

Your devices connect to each other directly whenever they can: over your local network, or over the internet when a direct path is possible. These connections are encrypted end to end (QUIC with TLS 1.3, with each device proving its identity inside the encrypted channel). No one in between, including us, can read them.

To find each other on a local network, devices announce themselves with names that change every 10 minutes and that only your other devices can recognize. Other people on the same network can see that a device is running Portide, but not whose device it is.

## 4. Relays

When two of your devices can't reach each other directly, they can use a **relay**: a server that passes along their encrypted traffic. A relay can't decrypt anything. You can run your own relay, use one someone else runs, or (when it is available) use the official Portide relay operated by us.

**What any relay can see** while your device is connected to it:

- your device's public IP address and port;
- a routing ID (a one-way hash of your device's public key) and the device's public key;
- which routing IDs open connections to each other;
- when connections start and end, and how many bytes pass through.

A relay can't see file contents, file names, text, device names or the list of devices in your circle, and it doesn't store anything it forwards.

**The official Portide relay** isn’t running yet. When it launches, it will keep this information as follows:

- IP addresses, routing IDs and public keys are held in memory only while your device is connected, and are not written to logs.
- The relay logs only aggregate counts every 10 minutes (number of devices online, number of connections, total bytes forwarded). These contain no IP addresses or device identifiers and are kept for up to 30 days.
- If we have to investigate a fault, we may briefly turn on detailed logging, which records connection events with IP addresses and the first characters of routing IDs. Detailed logs are deleted within 7 days and are used only to fix the fault.
- Access tokens are stored only as hashes.
- Our hosting provider may keep its own network logs under its own policy. We'll name the provider and server regions here before the official relay launches.

If you use a relay run by someone else, that operator's practices apply, not ours.

## 5. Update checks

Release versions of the desktop app and the `portide` command line check for a newer version at most once a day by asking GitHub (operated by GitHub, Inc.) for the latest Portide release. GitHub receives your IP address and a "Portide/version" identifier, and handles them under the [GitHub Privacy Statement](https://docs.github.com/site-policy/privacy-policies/github-general-privacy-statement). Nothing else is sent, and we don't receive any of it. The app only tells you a new version exists; it doesn't download or install anything.

You can turn this off in Settings › About (desktop) or by setting `PORTIDE_NO_UPDATE_CHECK=1` (command line). The iOS app doesn't check; the App Store handles its updates.

## 6. When you contact us

If you email us, report a bug, or sign up to be invited to the official relay, we receive your email address and whatever you choose to tell us. We use it only to reply, to fix the problem, or to send the invitation, and we delete it when it's no longer needed for that, or sooner if you ask. Invitation sign-ups are deleted within 30 days after the official relay opens to everyone or the program ends.

If you report a problem on GitHub, what you post is public and handled under GitHub's policies.

## 7. App stores and downloads

When you download Portide from the App Store or from GitHub, Apple or GitHub process information about the download under their own privacy policies. Apple may share anonymous statistics and crash reports with us only if you opt in on your device; we use them only to improve Portide.

## 8. Sharing and selling

We don't sell or rent personal information, and we don't share it with anyone for advertising. We would disclose information only if the law requires it, and given how Portide works we hold very little that could be disclosed. We can't hand over your files or messages because we never have them.

## 9. Your rights

Depending on where you live (for example under the EU and UK GDPR or California law), you may have the right to access, correct or delete personal information about you, to object to its use, or to complain to a data protection authority. Because the apps don't send us personal information, most requests concern only emails or sign-ups you sent us. Email us at the address below and we'll respond within 30 days.

Where the GDPR applies, we rely on our legitimate interest in running and securing the relay service and replying to messages, and on your consent for invitation sign-ups, which you can withdraw at any time.

## 10. Children

Portide isn't directed at children under 13 (or the minimum age in your country), and we don't knowingly collect information from them.

## 11. Changes

If we change this policy, we'll update the date at the top and describe significant changes in the release notes. If a change affects the official relay, we'll also say so in the app before it takes effect.

## 12. Contact

[YOUR LEGAL NAME]
Email: e7coding.apple@gmail.com
