# Portide for desktop (Windows, macOS, Linux)

Portide sends files and text between your own devices. The desktop app shows your conversations (one per device), transfers in progress, your devices and settings. Closing the window keeps Portide running in the tray, so it stays connected and keeps receiving; choose **Quit** from the tray menu to exit.

The desktop app works with the iOS app and with the `portide` command line: they share the same protocol and can all be in the same circle.

中文说明见同目录下的 `桌面端.md`。

## Install

### Windows (64-bit, Windows 10 / 11)

- **Installer** (`…-windows-amd64-setup.exe`): installs to Program Files\Portide and adds a Start menu shortcut (a desktop shortcut is optional). The installer isn't code-signed yet, so Windows SmartScreen says "unknown publisher": click **More info › Run anyway**.
- **Portable** (`…-windows-amd64.zip`): unzip and run `portide-desktop.exe`.
- Needs the Microsoft Edge WebView2 Runtime, which Windows 11 and updated Windows 10 already have. The installer tells you where to get it if it's missing.
- The first time Portide runs, Windows Firewall asks whether to allow it. Allow **private networks**, or other devices on your network can't reach this computer.
- **Upgrade**: quit Portide from the tray menu, then run the new installer.
- **Uninstall**: quit Portide from the tray menu, then Settings › Apps › Portide › Uninstall. Your data (`%APPDATA%\portide`) is kept, so reinstalling brings back your devices and history. Delete that folder if you don't need it.

### macOS (Apple silicon or Intel, macOS 11 or later)

- Open the `.dmg` and drag **Portide.app** to Applications.
- The app isn't signed and notarized by Apple yet. The first time you open it, macOS says it can't verify the developer: go to **System Settings › Privacy & Security** and click **Open Anyway**.
- Allow **Local Network** (to find your other devices) and **Notifications** when asked.
- The tray icon is in the menu bar. Data lives in `~/Library/Application Support/portide`.

### Linux (x86-64)

- **Ubuntu / Debian**: `sudo apt install ./portide-desktop-<version>-linux-amd64.deb`. Portide appears in the app menu. Uninstall with `sudo apt remove portide-desktop` (your data in `~/.config/portide` is kept).
- **Other distributions**: unpack the `.tar.gz` and run `portide-desktop`. Needs GTK 3 and WebKitGTK 4.1 (Fedora: `sudo dnf install gtk3 webkit2gtk4.1`).
- **Tray**: works out of the box on KDE and Ubuntu's desktop. Stock GNOME needs the AppIndicator extension (`gnome-shell-extension-appindicator`). Without a tray, closing the window quits Portide so it doesn't run in the background unseen.

## First launch

1. Click **Get started**.
2. If there's no system keychain (for example on Linux without a GNOME keyring session), set a passphrase. You'll enter it each time Portide starts. Windows and macOS always have a keychain, so this step is skipped there.
3. Name this device. Other devices in your circle see this name; you can change it later.

Portide starts with a circle that contains only this device. To add another device, open **Add device** on both: choose **Show code** on one and **Enter code** on the other, then type the 8 digits. (On the command line: `portide pair` and `portide join`.) If the devices can't find each other, click **Can't find the other device?** and enter one of the other device's addresses, listed under Settings › Network & relays › This device's addresses.

A device that is on its own joins the other device's circle, whichever side enters the code. If both sides already have other devices, the two circles merge: the circle of the device entering the code joins the circle of the device showing it. Portide explains what will happen before you confirm. Devices that weren't online during the merge show **Waiting to merge** and follow automatically the next time they connect to any merged device, for up to 30 days.

## Everyday use

- **Conversations**: one per device, plus **All devices**, which shows everything. Whatever you send in a device's conversation goes to that device; in All devices, pick one or more devices above the message box first.
- **Sending**: type and press Enter (Shift+Enter for a new line), use the paperclip for files or the folder button for folders, or paste an image with Ctrl+V. You can also drag files onto the conversation, or onto a device in the list.
- **Offline devices**: what you send waits in a queue and goes out when the device comes online.
- **Transfers**: pause, resume or cancel sends (paused sends never expire and resume where they stopped), or stop a receive.
- **Search**: Ctrl+K searches messages and file names.
- **Devices**: rename a device, change its icon (only on this computer), see its fingerprint, or remove it from the circle (for example a lost phone).
- **Relays**: under Settings › Network & relays you can add up to 4 relays to connect devices across networks. Relay settings sync to every device in the circle. See the relay guide for running your own.
- **Language**: English or Chinese. Portide follows the system language by default; change it under Settings › Appearance › Language. The tray menu and notifications switch too.
- **Start at login**: Settings › Notifications & startup.

Keyboard shortcuts: Ctrl+1 / 2 / 3 / 4 switch pages, Ctrl+K searches, Esc closes dialogs.

**Updates.** Once a day Portide asks GitHub whether a newer release exists. When one does, a dot appears on Settings and **Settings › About** offers a download; install the new version over the old one and your data and settings stay. Portide doesn't update itself. GitHub sees your IP address; nothing else is sent. Turn it off with **Check for updates automatically** in Settings › About.

## Using it with the command line

- The desktop app and `portide` use the same data folder: `~/.config/portide` on Linux, `%APPDATA%\portide` on Windows, `~/Library/Application Support/portide` on macOS. Set `PORTIDE_HOME` to use another one.
- While the desktop app is running, don't also run `portide daemon` on the same data folder. Other commands (`portide send`, `portide devices`, `portide history` and so on) work normally, and the window shows their results within a few seconds.

## Known limitations

- Merging two circles that use different relays only works for devices that can also reach each other on the local network or at a known address.
- Renaming isn't possible while a device in the circle still runs an older version that doesn't understand renames.
- You can't drop files on the tray icon, only on the window.
- If the port is taken (for example by another copy of Portide), the startup screen offers **Start on another port**. Devices where you typed this computer's address by hand need the new one.
