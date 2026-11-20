// 变化信号（M3a 设计 2.1）：Go 端按主题发出 "portide:changed" 事件，这里让对应的查询失效、重新拉取；
// 另外每 3 秒让全部查询失效一次，接住命令行进程对同一数据目录的改动（那些改动不会产生本进程的事件）。
import { useEffect } from 'react';
import { useQueryClient, type QueryClient } from '@tanstack/react-query';
import { Events } from '@wailsio/runtime';

export const CHANGED_EVENT = 'portide:changed';
export const FALLBACK_REFRESH_MS = 3000;

// 每个主题影响哪些查询（查询键的第一段）。时间线和会话列表由历史、发送任务和设备名拼成，三个主题都要刷新它们；
// 传输页由发送任务、正在进行的接收和设备名拼成。搜索结果跟着历史变。
export const TOPIC_QUERIES: Record<string, string[]> = {
  devices: ['devices', 'conversations', 'timeline', 'transfers'],
  transfers: ['conversations', 'timeline', 'transfers'],
  history: ['conversations', 'timeline', 'search'],
  inbox: ['inbox'],
  pairing: ['pairing', 'devices'],
  relay: ['relay'],
  settings: ['settings'],
};

export function invalidateTopic(qc: QueryClient, topic: string) {
  for (const key of TOPIC_QUERIES[topic] ?? []) {
    void qc.invalidateQueries({ queryKey: [key] });
  }
}

/** 在应用根部调用一次：订阅变化事件并开启兜底刷新。 */
export function usePortideChanges() {
  const qc = useQueryClient();
  useEffect(() => {
    const off = Events.On(CHANGED_EVENT, (ev) => invalidateTopic(qc, String(ev.data)));
    // 缩略图不参与兜底刷新：它们很贵，而且只随文件变化。
    const timer = window.setInterval(() => void qc.invalidateQueries({ predicate: (q) => q.queryKey[0] !== 'thumb' }), FALLBACK_REFRESH_MS);
    return () => {
      off();
      window.clearInterval(timer);
    };
  }, [qc]);
}
