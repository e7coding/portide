#!/bin/sh
# 把 docs/legal/ 里的隐私政策和使用条款转成网页：site/public/en/privacy.html、terms.html。
# 改了 docs/legal/*.md 之后重新运行一次。需要 pandoc。
#   sh site/build-legal.sh
set -eu
root=$(cd "$(dirname "$0")/.." && pwd)
out="$root/site/public/en"
for name in privacy terms; do
	src="$root/docs/legal/$name.md"
	title=$(grep -m1 '^# ' "$src" | sed 's/^# //')
	# 去掉文件开头写给自己看的 HTML 注释；把指向仓库里文件的链接换成网页或公开地址
	perl -0pe 's/\A<!--.*?-->\s*//s' "$src" |
		sed -e 's#](privacy\.md)#](privacy.html)#g' \
			-e 's#](terms\.md)#](terms.html)#g' \
			-e 's#](\.\./\.\./LICENSE)#](https://www.apache.org/licenses/LICENSE-2.0)#g' \
			-e 's#](\.\./\.\./core/relayserver/LICENSE)#](https://www.gnu.org/licenses/agpl-3.0.html)#g' \
			-e 's#\[Contributor License Agreement\](\.\./\.\./CLA\.md)#Contributor License Agreement in the Portide repository#g' |
		pandoc -f gfm -t html5 --template "$root/site/legal-template.html" \
			--metadata pagetitle="$title · Portide" -o "$out/$name.html"
	echo "$out/$name.html"
done
if grep -l '\[YOUR ' "$out/privacy.html" "$out/terms.html" >/dev/null; then
	echo "注意：页面里还有 [YOUR LEGAL NAME] / [YOUR COUNTRY / STATE] 占位，发布前先在 docs/legal/ 里填好再运行。" >&2
fi
