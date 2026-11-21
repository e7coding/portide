#!/usr/bin/env bash
# 构建桌面端 portide-desktop 并打包到 dist/（M3b 设计 4）：
#   bash scripts/build-desktop.sh [linux|windows|all|mac]
# 版本号取自 core/version/VERSION（见 scripts/version.sh）。
# Linux（x86-64）：.deb 和 tar.gz，需要 libgtk-3-dev、libwebkit2gtk-4.1-dev、dpkg-deb。
# Windows（x86-64）：交叉编译（不需要 cgo），NSIS 安装程序和免安装 zip，需要 makensis。
# macOS：只能在 Mac 上打（Wails 在 macOS 上需要 cgo），生成 Portide.app 和 .dmg。签名和公证由环境变量决定：
#   PORTIDE_MAC_SIGN_IDENTITY  钥匙串里 "Developer ID Application" 证书的名字或 SHA-1；不设时用临时签名（别的 Mac 打不开）
#   PORTIDE_NOTARY_KEY         App Store Connect API 密钥（.p8 文件路径），和下面两个一起设置时提交苹果公证并把票据钉进 dmg
#   PORTIDE_NOTARY_KEY_ID      密钥 ID
#   PORTIDE_NOTARY_ISSUER      Issuer ID
# 都需要 Go 和 pnpm。
set -euo pipefail
cd "$(dirname "$0")/.."
source scripts/version.sh
targets="${1:-all}"
# Linux 包的维护者（Debian 要求"名字 <邮箱>"）。正式发布前设成真实的联系邮箱。
maintainer="${PORTIDE_MAINTAINER:-Portide <portide@localhost>}"

(cd desktop/frontend && pnpm install --frozen-lockfile && pnpm build)

build_linux() {
  local name="portide-desktop-$version-linux-amd64"
  local out="dist/$name"
  rm -rf "$out" "$out.tar.gz" "dist/$name.deb"
  mkdir -p "$out"
  # Wails v3 beta 在 Linux 上默认用 GTK4，Portide 用 GTK3 + WebKitGTK 4.1。
  (cd desktop && CGO_ENABLED=1 GOOS=linux GOARCH=amd64 go build -tags gtk3 -trimpath -ldflags "$ldflags" -o "../$out/portide-desktop" .)
  cp docs/桌面端.md "$out/" && cp docs/en/desktop.md "$out/README.md"
  tar -C dist -czf "$out.tar.gz" "$name"

  # .deb：程序在 /opt/portide，启动器和各尺寸图标放到系统目录。Debian 用 ~ 表示预发布（0.6.0~beta.1 排在 0.6.0 前面）。
  local deb="dist/deb-$name"
  local tilde="~" debver
  debver="${version/-/$tilde}"
  rm -rf "$deb"
  install -Dm755 "$out/portide-desktop" "$deb/opt/portide/portide-desktop"
  install -Dm644 docs/桌面端.md "$deb/opt/portide/桌面端.md"
  install -Dm644 docs/en/desktop.md "$deb/opt/portide/README.md"
  install -Dm644 desktop/build/linux/portide.desktop "$deb/usr/share/applications/portide.desktop"
  for size in 16 32 48 64 128 256 512; do
    install -Dm644 "desktop/build/icons/$size.png" "$deb/usr/share/icons/hicolor/${size}x${size}/apps/portide.png"
  done
  install -d -m755 "$deb/usr/bin" "$deb/DEBIAN"
  ln -s /opt/portide/portide-desktop "$deb/usr/bin/portide-desktop"
  cat > "$deb/DEBIAN/control" <<CONTROL
Package: portide-desktop
Version: $debver
Architecture: amd64
Maintainer: $maintainer
Depends: libgtk-3-0t64 | libgtk-3-0, libwebkit2gtk-4.1-0
Section: net
Priority: optional
Description: Send files and text between your own devices
 Portide sends files and text between your own computers and phones,
 end to end encrypted. Devices on the same network connect directly;
 across networks they go through a relay you run yourself.
CONTROL
  dpkg-deb --root-owner-group --build "$deb" "dist/$name.deb"
  rm -rf "$deb"
  echo "built $out.tar.gz dist/$name.deb"
}

build_windows() {
  local name="portide-desktop-$version-windows-amd64"
  local out="dist/$name"
  rm -rf "$out" "dist/$name.zip" "dist/$name-setup.exe"
  mkdir -p "$out"
  # 图标、版本信息和清单编进 exe（.syso 只在构建时存在）。不用 local：EXIT 陷阱在函数
  # 因 set -e 提前退出时也要能看到这两个变量，才能在构建失败时也清理掉。
  syso="desktop/portide_windows_amd64.syso"
  info="$(mktemp)"
  trap 'rm -f "$syso" "$info"' EXIT
  cat > "$info" <<INFO
{"fixed": {"file_version": "$version_num.0", "product_version": "$version_num.0"},
 "info": {"0409": {"ProductName": "Portide", "ProductVersion": "$version", "FileVersion": "$version", "FileDescription": "Portide", "LegalCopyright": "", "CompanyName": "", "Comments": "Send files and text between your own devices"}}}
INFO
  (cd desktop && go run -tags gtk3 github.com/wailsapp/wails/v3/cmd/wails3 generate syso -arch amd64 \
    -icon build/windows/icon.ico -manifest build/windows/portide-desktop.exe.manifest -info "$info" -out portide_windows_amd64.syso)
  (cd desktop && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "$ldflags -H windowsgui" -o "../$out/portide-desktop.exe" .)
  cp docs/桌面端.md "$out/" && cp docs/en/desktop.md "$out/README.md"
  (cd dist && python3 -m zipfile -c "$name.zip" "$name")
  # makensis 在非 UTF-8 的 locale（例如 C）下读不了含中文的脚本，报 "Bad text encoding"：找一个 UTF-8 locale 给它。
  local utf8
  utf8="$(locale -a 2>/dev/null | grep -i -E '^(C|en_US)\.utf-?8$' | head -n1 || true)"
  [ -n "$utf8" ] || utf8=C.UTF-8
  (cd desktop/build/windows && LC_ALL="$utf8" makensis -V2 -INPUTCHARSET UTF8 -DVERSION="$version" -DVERSION_NUM="$version_num" -DSRC="$(cd ../../.. && pwd)/$out" \
    -DOUT="$(cd ../../.. && pwd)/dist/$name-setup.exe" installer.nsi)
  echo "built dist/$name.zip dist/$name-setup.exe"
}

# 带苹果时间戳签名：时间戳服务偶尔连不上（"The timestamp service is not available"），重试几次。
sign_timestamped() {
  local n
  for n in 1 2 3 4; do
    codesign --force --timestamp "$@" && return 0
    echo "codesign 失败，${n}0 秒后重试" >&2
    sleep "${n}0"
  done
  return 1
}

# make_dmg 把 Portide.app 做成"拖进应用程序文件夹"的 dmg：窗口里左边是 Portide，右边是 Applications 的替身，
# 中间是箭头背景（desktop/build/macos）。窗口布局要靠 Finder 写进 .DS_Store；Finder 脚本失败时（没有图形会话、
# 没有自动化权限）照样出 dmg，只是图标按默认排列，Applications 替身还在。
make_dmg() {
  local app="$1" dmg="$2" stage rw mnt bg=desktop/build/macos
  stage="$(mktemp -d)"
  rw="$(mktemp -d)/portide-rw.dmg"
  cp -R "$app" "$stage/"
  ln -s /Applications "$stage/Applications"
  mkdir "$stage/.background"
  # 一个 TIFF 里放 1x 和 2x 两张，Retina 屏上背景也清晰
  tiffutil -cathidpicheck "$bg/dmg-background.png" "$bg/dmg-background-2x.png" -out "$stage/.background/background.tiff" >/dev/null
  hdiutil create -volname Portide -srcfolder "$stage" -fs HFS+ -format UDRW -ov "$rw" >/dev/null
  mnt="$(hdiutil attach "$rw" -readwrite -noverify -noautoopen -nobrowse | awk -F'\t' '/\/Volumes\//{print $NF; exit}')"
  if ! osascript - "$(basename "$mnt")" <<'APPLESCRIPT'
on run argv
  tell application "Finder"
    tell disk (item 1 of argv)
      open
      set w to container window
      set current view of w to icon view
      set toolbar visible of w to false
      set statusbar visible of w to false
      set bounds of w to {200, 120, 800, 528}
      set o to icon view options of w
      set arrangement of o to not arranged
      set icon size of o to 112
      set text size of o to 13
      set background picture of o to file ".background:background.tiff"
      set position of item "Portide.app" of w to {160, 175}
      set position of item "Applications" of w to {440, 175}
      update without registering applications
      delay 1
      close
    end tell
  end tell
end run
APPLESCRIPT
  then
    echo "warning: Finder 没能排好 dmg 窗口（没有图形会话或没有自动化权限），dmg 里的图标按默认排列" >&2
  fi
  sync
  hdiutil detach "$mnt" -quiet || { sleep 3; hdiutil detach "$mnt" -force -quiet; }
  hdiutil convert "$rw" -format UDZO -imagekey zlib-level=9 -ov -o "$dmg" >/dev/null
  rm -rf "$stage" "$(dirname "$rw")"
}

build_mac() {
  [ "$(uname)" = Darwin ] || { echo "macOS 包只能在 Mac 上打" >&2; exit 2; }
  local arch name out app iconset
  arch="$(go env GOARCH)"
  name="portide-desktop-$version-macos-$arch"
  out="dist/$name"
  app="$out/Portide.app"
  rm -rf "$out" "dist/$name.dmg"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  # cgo 的 C/Objective-C 代码默认按本机系统版本编译，旧 macOS 上会打不开：固定成和 LSMinimumSystemVersion 一样的 11.0。
  # 用 CGO_CFLAGS / CGO_LDFLAGS 而不只是 MACOSX_DEPLOYMENT_TARGET：Go 的构建缓存只认前者，否则会复用按新系统编译的缓存。
  local minos="-mmacosx-version-min=11.0"
  (cd desktop && MACOSX_DEPLOYMENT_TARGET=11.0 CGO_CFLAGS="${CGO_CFLAGS:--O2 -g} $minos" CGO_LDFLAGS="${CGO_LDFLAGS:--O2 -g} $minos" \
    CGO_ENABLED=1 go build -trimpath -ldflags "$ldflags" -o "../$app/Contents/MacOS/portide-desktop" .)

  # 图标：由 desktop/build/icons 的 PNG 组成 .icns。
  iconset="$(mktemp -d)/portide.iconset"
  mkdir -p "$iconset"
  for s in 16 32 128 256 512; do
    cp "desktop/build/icons/$s.png" "$iconset/icon_${s}x${s}.png"
    cp "desktop/build/icons/$((s * 2)).png" "$iconset/icon_${s}x${s}@2x.png"
  done
  iconutil -c icns "$iconset" -o "$app/Contents/Resources/portide.icns"

  # 通知要求程序在带包标识的 .app 里运行（Wails 的通知服务会检查）。
  cat > "$app/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>Portide</string>
  <key>CFBundleDisplayName</key><string>Portide</string>
  <key>CFBundleIdentifier</key><string>org.portide.desktop</string>
  <key>CFBundleExecutable</key><string>portide-desktop</string>
  <key>CFBundleIconFile</key><string>portide</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>$version_num</string>
  <key>CFBundleVersion</key><string>$build</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <key>NSHighResolutionCapable</key><true/>
  <key>CFBundleDevelopmentRegion</key><string>en</string>
  <key>CFBundleLocalizations</key><array><string>en</string><string>zh-Hans</string></array>
  <key>NSLocalNetworkUsageDescription</key><string>Portide finds and connects directly to your other devices on the local network.</string>
  <key>NSBonjourServices</key><array><string>_portide._udp</string><string>_portide-pair._udp</string></array>
</dict>
</plist>
PLIST
  # 系统权限提示按系统语言显示：英文是 Info.plist 里的原文，中文在 zh-Hans.lproj。
  mkdir -p "$app/Contents/Resources/en.lproj" "$app/Contents/Resources/zh-Hans.lproj"
  printf '"NSLocalNetworkUsageDescription" = "%s";\n' "Portide finds and connects directly to your other devices on the local network." \
    > "$app/Contents/Resources/en.lproj/InfoPlist.strings"
  printf '"NSLocalNetworkUsageDescription" = "%s";\n' "Portide 在局域网里寻找并直接连接你自己的其他设备。" \
    > "$app/Contents/Resources/zh-Hans.lproj/InfoPlist.strings"
  local identity="${PORTIDE_MAC_SIGN_IDENTITY:-}" dmg="dist/$name.dmg"
  if [ -n "$identity" ]; then
    # Developer ID 签名：公证要求打开 hardened runtime（--options runtime）并带苹果时间戳。
    # 包里只有一个可执行文件，不需要 --deep；WebKit 的 JIT 在系统自己的进程里，不需要额外 entitlements。
    sign_timestamped --options runtime --sign "$identity" "$app"
    codesign --verify --strict --verbose=2 "$app"
  else
    # 没有开发者证书：临时签名（ad-hoc）。只适合本机测试，别的 Mac 下载后会被系统拦下。
    echo "warning: PORTIDE_MAC_SIGN_IDENTITY 没有设置，Portide.app 只做了临时签名" >&2
    codesign --force --sign - "$app"
  fi
  make_dmg "$app" "$dmg"
  if [ -n "$identity" ]; then
    sign_timestamped --sign "$identity" "$dmg"
    if [ -n "${PORTIDE_NOTARY_KEY:-}" ] && [ -n "${PORTIDE_NOTARY_KEY_ID:-}" ] && [ -n "${PORTIDE_NOTARY_ISSUER:-}" ]; then
      # 提交整个 dmg（里面的 Portide.app 一起公证），等结果；被拒时打印苹果给的日志。
      local result id
      result="$(xcrun notarytool submit "$dmg" --key "$PORTIDE_NOTARY_KEY" --key-id "$PORTIDE_NOTARY_KEY_ID" \
        --issuer "$PORTIDE_NOTARY_ISSUER" --wait --timeout 1h --output-format json)" || true
      echo "$result"
      if ! printf '%s' "$result" | grep -q '"status" *: *"Accepted"'; then
        id="$(printf '%s' "$result" | sed -n 's/.*"id" *: *"\([^"]*\)".*/\1/p' | head -n1)"
        [ -z "$id" ] || xcrun notarytool log "$id" --key "$PORTIDE_NOTARY_KEY" --key-id "$PORTIDE_NOTARY_KEY_ID" --issuer "$PORTIDE_NOTARY_ISSUER" >&2 || true
        echo "苹果公证没有通过" >&2
        exit 1
      fi
      # 把公证票据钉进 dmg：离线时 Gatekeeper 也能验证。
      xcrun stapler staple "$dmg"
      xcrun stapler validate "$dmg"
      spctl --assess --type open --context context:primary-signature --verbose=2 "$dmg"
    else
      echo "warning: 没有设置 PORTIDE_NOTARY_KEY / PORTIDE_NOTARY_KEY_ID / PORTIDE_NOTARY_ISSUER，dmg 已签名但没有公证" >&2
    fi
  fi
  echo "built $dmg"
}

case "$targets" in
  linux) build_linux ;;
  windows) build_windows ;;
  all) build_linux; build_windows ;;
  mac) build_mac ;;
  *) echo "用法：bash scripts/build-desktop.sh [linux|windows|all|mac]" >&2; exit 2 ;;
esac
