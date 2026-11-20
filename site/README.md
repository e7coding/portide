# Portide 宣传站

纯静态页面，没有构建步骤。版式沿用 Kept 宣传页，首屏借鉴 PaywallPro 的扇形手机和浮动卡片。

```
public/
  index.html        按 localStorage 的 portide-lang 或浏览器语言跳到 zh/ 或 en/
  assets/           共用的样式 site.css、脚本 site.js（下载页另有 download.js）、图标
  en/ zh/           各语言的页面和截图（img/）：
                      index.html 首页，download.html 下载页，support.html 支持页
  en/privacy.html   隐私政策、使用条款：由 build-legal.sh 从 docs/legal/ 生成，不要手改
  en/terms.html
docker/             nginx 容器（compose.yml、nginx.conf）
```

## 本地预览

```bash
python3 -m http.server 8765 --bind 127.0.0.1 -d site/public
```

然后打开 http://127.0.0.1:8765/ 。直接双击 HTML 文件打开时样式加载不了，要用上面的方式。

## 发布前要做的

1. **仓库地址**：仓库公开后，在 `public/assets/site.js` 顶部填 `REPO`（例如 `https://github.com/owner/portide`）。留空时下载按钮显示"即将提供"，GitHub 和文档链接、报告问题入口隐藏。填好后下载页会向 GitHub 读最新的正式版本（不含草稿和预发布），按文件名给每一行填上真实的下载地址和大小；文件名规则改了的话，同步改下载页里每行的 `data-asset` 正则。
2. **法律文件**：在 `docs/legal/privacy.md`、`terms.md` 里填好 `[YOUR LEGAL NAME]` 和 `[YOUR COUNTRY / STATE]`，再运行 `sh site/build-legal.sh`（需要 pandoc）。隐私政策的网址（`…/en/privacy.html`）填进 App Store Connect。
3. **App Store**：App Store Connect 的"支持网址"填 `…/en/support.html`，"隐私政策网址"填 `…/en/privacy.html`。上架后把下载页（两种语言）iPhone 卡片的按钮换成 App Store 链接，首页下载区那张卡片去掉"即将上架"标签。

## 改版本号时

页面引用 `site.css`、`site.js`、`download.js` 时带着 `?v=4` 这样的版本号。nginx 已经让浏览器每次都先确认文件有没有变，但旧版本的缓存规则是一天，改了样式或脚本后把所有页面里的 `?v=` 加一，保证老访客也拿到新文件。

## 截图

截图来自 `ios/screenshots/<语言>/`（`bash scripts/screenshots-ios.sh`，中文加 `PORTIDE_SHOT_LANG=zh`），缩小后转成 JPG：

```bash
for lang in en zh; do
  for f in ios/screenshots/$lang/iphone/*.png; do b=$(basename "$f" .png); sips -s format jpeg -s formatOptions 80 --resampleWidth 600 "$f" --out "site/public/$lang/img/iphone-${b#0?-}.jpg"; done
  for f in ios/screenshots/$lang/ipad/*.png; do b=$(basename "$f" .png); sips -s format jpeg -s formatOptions 80 --resampleWidth 1000 "$f" --out "site/public/$lang/img/ipad-${b#0?-}.jpg"; done
done
```

## 部署

任意装了 Docker 的服务器：先把主机写进 `site/deploy.env`（`PORTIDE_SITE_HOST=root@…`，不进仓库），再运行 `sh site/deploy.sh`。它把 `public/` 和 `docker/` 同步到 `/data/portide-site`，启动 `portide-site` 容器，再逐页检查返回 200。SSH 端口、目录、站点端口也可以写在那里（见脚本开头）。之后只改页面时再运行一次即可，容器不用重启。

其他地方：把 `public/` 同步到任意静态托管（nginx、GitHub Pages、Cloudflare Pages 都行），或者在服务器上放 `docker/compose.yml`、`docker/nginx.conf` 和 `public/`，然后 `docker compose up -d`。
