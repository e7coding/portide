// 外观：浅色 / 深色 / 跟随系统。只影响本机界面，存在浏览器本地存储里（M3a 设计 3.3）。
import { createContext, useContext, useEffect, useMemo, useState, type ReactNode } from 'react';
import { CssBaseline, ThemeProvider } from '@mui/material';
import { buildTheme, localized } from '../theme';
import { useLang } from '../i18n';

export type ThemeMode = 'light' | 'dark' | 'system';
const STORAGE_KEY = 'portide.theme';

function loadMode(): ThemeMode {
  const v = localStorage.getItem(STORAGE_KEY);
  return v === 'light' || v === 'dark' ? v : 'system';
}

function systemPrefersDark(): boolean {
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false;
}

const Ctx = createContext<{ mode: ThemeMode; setMode: (m: ThemeMode) => void }>({ mode: 'system', setMode: () => {} });

export function useThemeMode() {
  return useContext(Ctx);
}

export function ThemeModeProvider({ children }: { children: ReactNode }) {
  const [mode, setModeState] = useState<ThemeMode>(loadMode);
  const [systemDark, setSystemDark] = useState(systemPrefersDark);
  useEffect(() => {
    const mq = window.matchMedia?.('(prefers-color-scheme: dark)');
    if (!mq) return;
    const onChange = () => setSystemDark(mq.matches);
    mq.addEventListener('change', onChange);
    return () => mq.removeEventListener('change', onChange);
  }, []);
  const resolved = mode === 'system' ? (systemDark ? 'dark' : 'light') : mode;
  const lang = useLang();
  const theme = useMemo(() => localized(buildTheme(resolved), lang), [resolved, lang]);
  const value = useMemo(
    () => ({
      mode,
      setMode: (m: ThemeMode) => {
        localStorage.setItem(STORAGE_KEY, m);
        setModeState(m);
      },
    }),
    [mode],
  );
  return (
    <Ctx.Provider value={value}>
      <ThemeProvider theme={theme}>
        <CssBaseline />
        {children}
      </ThemeProvider>
    </Ctx.Provider>
  );
}
