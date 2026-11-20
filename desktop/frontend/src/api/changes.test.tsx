import { renderHook } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import type { ReactNode } from 'react';
import { Events } from '@wailsio/runtime';
import { CHANGED_EVENT, FALLBACK_REFRESH_MS, invalidateTopic, usePortideChanges } from './changes';

function client() {
  const qc = new QueryClient();
  const spy = vi.spyOn(qc, 'invalidateQueries').mockResolvedValue();
  return { qc, spy };
}

test('a topic invalidates the queries built from it', () => {
  const { qc, spy } = client();
  invalidateTopic(qc, 'transfers');
  expect(spy.mock.calls).toEqual([[{ queryKey: ['conversations'] }], [{ queryKey: ['timeline'] }], [{ queryKey: ['transfers'] }]]);
  spy.mockClear();
  invalidateTopic(qc, 'devices');
  expect(spy.mock.calls).toEqual([[{ queryKey: ['devices'] }], [{ queryKey: ['conversations'] }], [{ queryKey: ['timeline'] }], [{ queryKey: ['transfers'] }]]);
  spy.mockClear();
  invalidateTopic(qc, 'history');
  expect(spy.mock.calls).toEqual([[{ queryKey: ['conversations'] }], [{ queryKey: ['timeline'] }], [{ queryKey: ['search'] }]]);
  spy.mockClear();
  invalidateTopic(qc, 'unknown');
  expect(spy).not.toHaveBeenCalled();
});

test('usePortideChanges subscribes to the change event and refreshes everything periodically', () => {
  vi.useFakeTimers();
  const { qc, spy } = client();
  const off = vi.fn();
  let handler: ((ev: { data: unknown }) => void) | undefined;
  vi.mocked(Events.On).mockImplementation(((name: string, cb: (ev: { data: unknown }) => void) => {
    expect(name).toBe(CHANGED_EVENT);
    handler = cb;
    return off;
  }) as unknown as typeof Events.On);
  const wrapper = ({ children }: { children: ReactNode }) => <QueryClientProvider client={qc}>{children}</QueryClientProvider>;
  const { unmount } = renderHook(() => usePortideChanges(), { wrapper });

  handler!({ data: 'inbox' });
  expect(spy).toHaveBeenCalledWith({ queryKey: ['inbox'] });
  spy.mockClear();
  vi.advanceTimersByTime(FALLBACK_REFRESH_MS);
  expect(spy).toHaveBeenCalledTimes(1);
  // 兜底刷新跳过缩略图（很贵，而且只随文件变化）。
  const { predicate } = spy.mock.calls[0][0] as unknown as { predicate: (q: { queryKey: unknown[] }) => boolean };
  expect(predicate({ queryKey: ['thumb', 1, 0] })).toBe(false);
  expect(predicate({ queryKey: ['timeline', ''] })).toBe(true);

  unmount();
  expect(off).toHaveBeenCalled();
  vi.useRealTimers();
});
