import type { ReactElement } from 'react';
import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ThemeModeProvider } from '../store/themeMode';
import { ToastProvider } from '../store/toast';
import { NavProvider, type Nav } from '../store/nav';

/** 在全新的 QueryClient 和主题里渲染（每个测试互不影响）。 */
export function renderWithProviders(ui: ReactElement) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return { client, ...render(<QueryClientProvider client={client}><ThemeModeProvider><ToastProvider>{ui}</ToastProvider></ThemeModeProvider></QueryClientProvider>) };
}

/** 和 renderWithProviders 一样，另外包上导航状态（可以指定初始页面和选中项）。 */
export function renderWithNav(ui: ReactElement, initial?: Partial<Pick<Nav, 'page' | 'conversation' | 'device' | 'section'>>) {
  return renderWithProviders(<NavProvider initial={initial}>{ui}</NavProvider>);
}

export const runningState = {
  phase: 'running', needsName: false, firstPassphrase: false, defaultName: 'host', name: '笔记本', keyId: 'AAAA1111BBBB2222', error: '', errorKind: '', detail: '', dataDir: '/home/me/.config/portide', nameSuggestions: [] as string[],
};
