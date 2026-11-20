// 界面语言的入口：先读 Go 端的偏好再渲染（单独一个文件，免得纯函数的 i18n 模块依赖 Go 绑定）。
import type { ReactNode } from 'react';
import { usePrefs } from '../api/queries';
import { LanguageProvider, normalizeLang } from './index';

/**
 * 读出 Go 端定下的界面语言后再渲染界面（偏好在本机文件里，几乎立即返回）；读不到时按浏览器语言。
 * 放在 QueryClientProvider 里面、主题外面（主题要按语言选 MUI 自带文字的语言）。
 */
export function LanguageGate({ children }: { children: ReactNode }) {
  const { data, isError } = usePrefs();
  if (!data && !isError) return null;
  const initial = data?.uiLanguage ? normalizeLang(data.uiLanguage) : normalizeLang(navigator.language);
  return <LanguageProvider initial={initial}>{children}</LanguageProvider>;
}
