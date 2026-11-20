// 翻译检查：源码里每个 t('…') 都有英文译文、占位符一致；没有漏包 t() 的中文；en.ts 里没有用不到的译文。
import { describe, expect, it } from 'vitest';
import { en } from './en';
import { interpolate, normalizeLang } from './index';

const HAN = /[一-鿿]/;
const KEY = /(?<![\w.])t\('((?:[^'\\]|\\.)*)'/g;

// 源码原文（由 Vite 在测试时读入）：除测试文件、test/ 和 i18n/ 以外的所有 .ts / .tsx。
const raw = import.meta.glob(['../**/*.{ts,tsx}', '!../**/*.test.{ts,tsx}', '!../test/**', '!../i18n/**'], { query: '?raw', import: 'default', eager: true }) as Record<string, string>;

const unescape = (s: string) => s.replace(/\\(.)/g, '$1');
const files = Object.entries(raw).map(([path, text]) => ({ path: path.replace(/^\.\.\//, ''), text }));
const keys = new Set(files.flatMap((f) => [...f.text.matchAll(KEY)].map((m) => unescape(m[1]))));
const placeholders = (s: string) => [...new Set([...s.matchAll(/\{(\w+)/g)].map((m) => m[1]))].sort();

/** 去掉注释（行尾 //、整行的 /* * 注释、JSX 的 {/* *\/}），粗略即可：中文注释里不会出现引号配对问题。 */
function code(line: string): string {
  const s = line.trim();
  if (s.startsWith('//') || s.startsWith('*') || s.startsWith('/*') || s.startsWith('{/*')) return '';
  return line.replace(/\{\/\*.*?\*\/\}/g, '').replace(/\s\/\/\s.*$/, '');
}

describe('界面翻译', () => {
  it('读到了源码', () => {
    expect(files.length).toBeGreaterThan(30);
  });

  it('每个 t() 的键都有英文译文，占位符一致', () => {
    const missing = [...keys].filter((k) => !(k in en));
    expect(missing).toEqual([]);
    const mismatched = [...keys].filter((k) => k in en && placeholders(k).join() !== placeholders(en[k]).join());
    expect(mismatched).toEqual([]);
  });

  it('译文里没有中文、没有用不到的译文', () => {
    expect(Object.entries(en).filter(([, v]) => HAN.test(v)).map(([k]) => k)).toEqual([]);
    expect(Object.keys(en).filter((k) => !keys.has(k))).toEqual([]);
  });

  it('源码里给用户看的中文都包在 t() 里', () => {
    const leftovers: string[] = [];
    for (const f of files) {
      f.text.split('\n').forEach((line, i) => {
        if (line.includes('i18n-ignore')) return;
        const rest = code(line).replace(KEY, '');
        if (HAN.test(rest)) leftovers.push(`${f.path}:${i + 1}: ${line.trim()}`);
      });
    }
    expect(leftovers).toEqual([]);
  });

  it('插值和单复数', () => {
    expect(interpolate('{n} {n|file|files}', { n: 1 })).toBe('1 file');
    expect(interpolate('{n} {n|file|files}', { n: 3 })).toBe('3 files');
    expect(interpolate('Sent to {name}', { name: 'Mac' })).toBe('Sent to Mac');
    expect(normalizeLang('zh-CN')).toBe('zh');
    expect(normalizeLang('en-US')).toBe('en');
    expect(normalizeLang('')).toBe('en');
  });
});
