#!/bin/sh
# 把宣传站部署到一台装了 Docker 的服务器（nginx 容器，见 site/docker）。
#   sh site/deploy.sh
# 环境变量：PORTIDE_SITE_HOST（必填，如 root@example.com）、PORTIDE_SITE_SSH_PORT（22）、
#           PORTIDE_SITE_DIR（/data/portide-site）、PORTIDE_SITE_PORT（8030）、
#           PORTIDE_SITE_PROJECT（Docker Compose 项目名，portide-site）
# 也可以写进 site/deploy.env（不进仓库），脚本启动时会先读它，例如：
#   PORTIDE_SITE_HOST=root@example.com
#   PORTIDE_SITE_SSH_PORT=22
set -eu
root=$(cd "$(dirname "$0")" && pwd)
if [ -f "$root/deploy.env" ]; then
  set -a; . "$root/deploy.env"; set +a
fi
host=${PORTIDE_SITE_HOST:?请设置 PORTIDE_SITE_HOST，或写进 site/deploy.env}
ssh_port=${PORTIDE_SITE_SSH_PORT:-22}
dir=${PORTIDE_SITE_DIR:-/data/portide-site}
port=${PORTIDE_SITE_PORT:-8030}
project=${PORTIDE_SITE_PROJECT:-portide-site}
ssh_cmd="ssh -p $ssh_port"

echo "同步 $root/public/ -> $host:$dir/public/"
$ssh_cmd "$host" "mkdir -p '$dir/public'"
rsync -rlz --delete --chmod=Da+rx,Fa+r --exclude .DS_Store -e "$ssh_cmd" "$root/public/" "$host:$dir/public/"
# 容器只挂载 public/，页面改动不用重启。nginx.conf 原地覆盖（--inplace，挂载进容器的还是同一个文件），再让 nginx 重新加载
rsync -rlz --inplace --chmod=Fa+r -e "$ssh_cmd" "$root/docker/compose.yml" "$root/docker/nginx.conf" "$host:$dir/"
$ssh_cmd "$host" "cd '$dir' && PORTIDE_SITE_PORT=$port docker compose -p '$project' up -d && docker compose -p '$project' exec -T web nginx -s reload"

url="http://${host#*@}:$port"
for p in '' zh/ en/ en/privacy.html; do
	code=$(curl -s --noproxy '*' -o /dev/null -w '%{http_code}' "$url/$p")
	echo "$code  $url/$p"
	[ "$code" = 200 ] || { echo "检查失败" >&2; exit 1; }
done
echo "完成"
