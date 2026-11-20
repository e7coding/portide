// 执行一个操作，失败时用红色提示条显示原因（按钮点击、菜单项等不需要等结果的操作都用它）。
import { useCallback } from 'react';
import { useToast } from '../store/toast';
import { errorText } from './errors';

export function useRun() {
  const toast = useToast();
  return useCallback(
    async <T,>(action: () => Promise<T>, success?: string): Promise<T | undefined> => {
      try {
        const res = await action();
        if (success) toast.success(success);
        return res;
      } catch (err) {
        toast.error(errorText(err));
        return undefined;
      }
    },
    [toast],
  );
}
