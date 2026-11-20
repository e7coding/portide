// 文字消息里的链接（M3c 设计 4.1）：识别 http / https 链接，点击后用系统浏览器打开（Platform.OpenURL 只放行这两种）。

export type TextPart = { text: string; url?: string };

// 链接到空白、引号或中文标点为止；末尾的英文句读不算在链接里。
const URL_RE = /https?:\/\/[^\s<>"'，。！？；：、（）【】「」]+/gi;
const TRAILING = /[.,;:!?)\]}]+$/;

export function linkify(text: string): TextPart[] {
  const parts: TextPart[] = [];
  let last = 0;
  for (const m of text.matchAll(URL_RE)) {
    let url = m[0];
    const trail = url.match(TRAILING)?.[0] ?? '';
    url = url.slice(0, url.length - trail.length);
    const start = m.index ?? 0;
    if (start > last) parts.push({ text: text.slice(last, start) });
    parts.push({ text: url, url });
    last = start + url.length;
  }
  if (last < text.length) parts.push({ text: text.slice(last) });
  return parts;
}
