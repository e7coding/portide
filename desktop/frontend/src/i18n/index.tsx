// 界面语言：中文原文就是键，代码里写 t('正在发送')，英文译文放在 en.ts。没有译文的句子原样显示中文；
// i18n.test.ts 保证每个 t('…') 都有译文、源码里没有漏包的中文。
//
// 插值：t('约 {n} 秒', { n: 5 })。英文的单复数写成 {n|file|files}：n 为 1 时取前一个词，否则取后一个。
// 语言由 Go 端决定（Platform.Prefs().uiLanguage：设置里选的语言，或者跟随系统），托盘、通知和错误提示用同一个。
import { createContext, useContext, useMemo, useState, type ReactNode } from 'react';
import { en } from './en';

export type Lang = 'zh' | 'en';
/** 设置里的选项：空串表示跟随系统。 */
export type LangSetting = '' | Lang;

let current: Lang = 'zh';

/** 当前界面语言（非组件代码用；组件里用 useLang，语言切换时才会重新渲染）。 */
export function lang(): Lang {
  return current;
}

/** 语言标签（"zh-CN"、"en-US"）归到支持的语言：zh 开头的是中文，其余都是英文。 */
export function normalizeLang(tag: string | undefined | null): Lang {
  return (tag ?? '').toLowerCase().startsWith('zh') ? 'zh' : 'en';
}

type Params = Record<string, string | number>;

/** zh 在当前语言下的文字。 */
export function t(zh: string, params?: Params): string {
  const s = current === 'en' ? (en[zh] ?? zh) : zh;
  return params ? interpolate(s, params) : s;
}

export function interpolate(s: string, params: Params): string {
  return s.replace(/\{(\w+)(?:\|([^|}]*)\|([^}]*))?\}/g, (whole, name: string, one?: string, other?: string) => {
    if (!(name in params)) return whole;
    const v = params[name];
    if (one === undefined) return String(v);
    return Number(v) === 1 ? one : (other ?? '');
  });
}

const Ctx = createContext<{ lang: Lang; setLang: (l: Lang) => void }>({ lang: 'zh', setLang: () => {} });

/** 当前语言；组件调用它之后，切换语言时会跟着重新渲染。 */
export function useLang(): Lang {
  return useContext(Ctx).lang;
}

/** 组件里取翻译函数：同 t，但会让组件在切换语言时重新渲染。 */
export function useT(): typeof t {
  useContext(Ctx);
  return t;
}

/** 切换界面语言（只改界面这一侧；Go 端由 Platform.SetLanguage 改）。 */
export function useSetLang(): (l: Lang) => void {
  return useContext(Ctx).setLang;
}

export function LanguageProvider({ initial, children }: { initial: Lang; children: ReactNode }) {
  const [value, setValue] = useState<Lang>(() => {
    current = initial;
    document.documentElement.lang = initial === 'zh' ? 'zh-CN' : 'en';
    return initial;
  });
  const ctx = useMemo(
    () => ({
      lang: value,
      setLang: (l: Lang) => {
        current = l;
        document.documentElement.lang = l === 'zh' ? 'zh-CN' : 'en';
        setValue(l);
      },
    }),
    [value],
  );
  return <Ctx.Provider value={ctx}>{children}</Ctx.Provider>;
}

/** 测试用：直接设置语言（不经过 Provider）。 */
export function setLangForTest(l: Lang) {
  current = l;
}

