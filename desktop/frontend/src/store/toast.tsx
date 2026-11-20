// 轻提示（M3c 设计 4.6）：成功、警告、错误三种样式，在窗口底部居中出现几秒后消失。
// 错误用红色（M3b 里一律用黄色）。用法：const toast = useToast(); toast.error(errorText(err))。
import { createContext, useCallback, useContext, useMemo, useState, type ReactNode } from 'react';
import { Box, Snackbar, Typography } from '@mui/material';
import { CircleAlert, CircleCheck, TriangleAlert } from 'lucide-react';

export type ToastKind = 'success' | 'warning' | 'error';

interface Toast {
  id: number;
  kind: ToastKind;
  text: string;
}

interface ToastApi {
  success: (text: string) => void;
  warning: (text: string) => void;
  error: (text: string) => void;
}

const Ctx = createContext<ToastApi>({ success: () => {}, warning: () => {}, error: () => {} });

export function useToast() {
  return useContext(Ctx);
}

/** 每种提示停留的时间（毫秒）：错误和警告要留够读完的时间。 */
export const TOAST_MS: Record<ToastKind, number> = { success: 2500, warning: 6000, error: 8000 };

const STYLE: Record<ToastKind, { icon: typeof CircleCheck; color: string; bg: string }> = {
  success: { icon: CircleCheck, color: 'portide.ok', bg: 'portide.okSoft' },
  warning: { icon: TriangleAlert, color: 'portide.warn', bg: 'portide.warnSoft' },
  error: { icon: CircleAlert, color: 'portide.err', bg: 'portide.errSoft' },
};

export function ToastProvider({ children }: { children: ReactNode }) {
  const [current, setCurrent] = useState<Toast | null>(null);
  const show = useCallback((kind: ToastKind, text: string) => setCurrent({ id: Date.now() + Math.random(), kind, text }), []);
  const api = useMemo<ToastApi>(
    () => ({ success: (t) => show('success', t), warning: (t) => show('warning', t), error: (t) => show('error', t) }),
    [show],
  );
  const style = current ? STYLE[current.kind] : null;
  return (
    <Ctx.Provider value={api}>
      {children}
      <Snackbar
        key={current?.id}
        open={current !== null}
        autoHideDuration={current ? TOAST_MS[current.kind] : null}
        onClose={(_, reason) => reason !== 'clickaway' && setCurrent(null)}
        anchorOrigin={{ vertical: 'bottom', horizontal: 'center' }}
      >
        {current && style ? (
          <Box
            role={current.kind === 'success' ? 'status' : 'alert'}
            data-kind={current.kind}
            sx={{
              display: 'flex', alignItems: 'center', gap: 1, px: 1.75, py: 1, borderRadius: '10px', maxWidth: 520,
              bgcolor: 'portide.bg', border: 1, borderColor: 'portide.line', boxShadow: 2,
            }}
          >
            <Box sx={{ width: 24, height: 24, borderRadius: '7px', display: 'grid', placeItems: 'center', bgcolor: style.bg, color: style.color, flexShrink: 0 }}>
              <style.icon size={15} />
            </Box>
            <Typography variant="body2" sx={{ color: 'portide.fg' }}>
              {current.text}
            </Typography>
          </Box>
        ) : undefined}
      </Snackbar>
    </Ctx.Provider>
  );
}
