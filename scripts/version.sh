# 构建脚本共用的版本号（用 source 引入，当前目录须是仓库根目录）。唯一的来源是 core/version/VERSION：
#   version      完整版本，例如 0.5.0 或 0.6.0-beta.1（安装包文件名、Linux 包版本）
#   version_num  去掉预发布标记的数字部分，例如 0.6.0（Windows 文件版本、macOS / iOS 的 CFBundleShortVersionString）
#   build        构建号：默认是 git 提交数，只增不减（macOS / iOS 的 CFBundleVersion）；可用 PORTIDE_BUILD 指定
version="$(tr -d '[:space:]' < core/version/VERSION)"
version_num="${version%%-*}"
build="${PORTIDE_BUILD:-$(git rev-list --count HEAD 2>/dev/null || echo 1)}"
#   ldflags      Go 链接参数：去掉调试信息；设置了 PORTIDE_UPDATE_REPO（GitHub 仓库 "owner/name"）时写入检查更新用的仓库，
#                发布流程里是当前仓库。不设置时（本地自己编译）桌面端和命令行不检查更新。
ldflags="-s -w"
if [ -n "${PORTIDE_UPDATE_REPO:-}" ]; then
  if ! [[ "$PORTIDE_UPDATE_REPO" =~ ^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$ ]]; then
    echo "PORTIDE_UPDATE_REPO 应当是 owner/name：$PORTIDE_UPDATE_REPO" >&2
    exit 1
  fi
  ldflags="$ldflags -X portide/core/update.Repo=$PORTIDE_UPDATE_REPO"
fi
