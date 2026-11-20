import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { LanguageGate } from './i18n/gate';
import { ThemeModeProvider } from './store/themeMode';
import { ToastProvider } from './store/toast';
import { App } from './App';

const queryClient = new QueryClient({
  // 数据都在本机，失败一般是"核心还没启动"之类，重试没有意义；刷新靠变化信号和兜底定时器。
  defaultOptions: { queries: { retry: false, refetchOnWindowFocus: false } },
});

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <LanguageGate>
        <ThemeModeProvider>
          <ToastProvider>
            <App />
          </ToastProvider>
        </ThemeModeProvider>
      </LanguageGate>
    </QueryClientProvider>
  </StrictMode>,
);
