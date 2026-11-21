#!/bin/sh
# 部署宣传站。两种方式：
#   docker：服务器上用 Docker 跑一个 nginx 容器（见 site/docker），页面放在 $PORTIDE_SITE_DIR/public
#   static：服务器上已经有 nginx，只把页面同步到 $PORTIDE_SITE_DIR，nginx 的配置自己维护
#   sh site/deploy.sh
# 环境变量：PORTIDE_SITE_HOST（必填，如 root@example.com，或 ~/.ssh/config 里的别名）
#           PORTIDE_SITE_MODE（docker 或 static，默认 docker）
#           PORTIDE_SITE_SSH_PORT（不设时用 ssh 的默认值或 ~/.ssh/config 里的设置）
#           PORTIDE_SITE_DIR（/data/portide-site）
#           PORTIDE_SITE_URL（部署后检查的地址；docker 方式默认 http://<主机>:$PORTIDE_SITE_PORT）
#           docker 方式另有 PORTIDE_SITE_PORT（8030）、PORTIDE_SITE_PROJECT（Docker Compose 项目名，portide-site）
# 也可以写进 site/deploy.env（不进仓库），脚本启动时会先读它，例如：
#   PORTIDE_SITE_HOST=myserver
#   PORTIDE_SITE_MODE=static
#   PORTIDE_SITE_DIR=/data/portide-site
#   PORTIDE_SITE_URL=https://example.com/portide
set -eu
root=$(cd "$(dirname "$0")" && pwd)
if [ -f "$root/deploy.env" ]; then
  set -a; . "$root/deploy.env"; set +a
fi
host=${PORTIDE_SITE_HOST:?请设置 PORTIDE_SITE_HOST，或写进 site/deploy.env}
mode=${PORTIDE_SITE_MODE:-docker}
dir=${PORTIDE_SITE_DIR:-/data/portide-site}
ssh_cmd="ssh"
[ -z "${PORTIDE_SITE_SSH_PORT:-}" ] || ssh_cmd="ssh -p $PORTIDE_SITE_SSH_PORT"

case "$mode" in
static)
  echo "同步 $root/public/ -> $host:$dir/"
  $ssh_cmd "$host" "mkdir -p '$dir'"
  rsync -rlz --delete --chmod=Da+rx,Fa+r --exclude .DS_Store -e "$ssh_cmd" "$root/public/" "$host:$dir/"
  url=${PORTIDE_SITE_URL:-}
  ;;
docker)
  port=${PORTIDE_SITE_PORT:-8030}
  project=${PORTIDE_SITE_PROJECT:-portide-site}
  echo "同步 $root/public/ -> $host:$dir/public/"
  $ssh_cmd "$host" "mkdir -p '$dir/public'"
  rsync -rlz --delete --chmod=Da+rx,Fa+r --exclude .DS_Store -e "$ssh_cmd" "$root/public/" "$host:$dir/public/"
  # 容器只挂载 public/，页面改动不用重启。nginx.conf 原地覆盖（--inplace，挂载进容器的还是同一个文件），再让 nginx 重新加载
  rsync -rlz --inplace --chmod=Fa+r -e "$ssh_cmd" "$root/docker/compose.yml" "$root/docker/nginx.conf" "$host:$dir/"
  $ssh_cmd "$host" "cd '$dir' && PORTIDE_SITE_PORT=$port docker compose -p '$project' up -d && docker compose -p '$project' exec -T web nginx -s reload"
  url=${PORTIDE_SITE_URL:-http://${host#*@}:$port}
  ;;
*) echo "PORTIDE_SITE_MODE 应当是 docker 或 static：$mode" >&2; exit 2 ;;
esac

if [ -z "$url" ]; then
  echo "完成（没有设置 PORTIDE_SITE_URL，跳过检查）"
  exit 0
fi
url=${url%/}
# -k：还没有域名时用 IP 访问，服务器上是自签名证书
for p in '' zh/ en/ en/download.html en/privacy.html assets/site.js; do
	code=$(curl -sk --noproxy '*' -o /dev/null -w '%{http_code}' "$url/$p")
	echo "$code  $url/$p"
	[ "$code" = 200 ] || { echo "检查失败" >&2; exit 1; }
done
echo "完成"
