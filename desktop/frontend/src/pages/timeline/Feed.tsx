// 消息流（M3c 设计 4.1）：日期分隔、连续消息合并（见 grouping.ts）；整块是拖放目标，文件拖进来时盖上一层遮罩
// "松开发给「台式机」"。搜索跳转过来时把那一条滚到中间并高亮几秒。
import { useEffect, useMemo, useRef, type ReactNode } from 'react';
import { Box, Typography } from '@mui/material';
import { Upload } from 'lucide-react';
import type { TimelineItem } from '../../api/portide';
import { groupFeed } from './grouping';
import { MessageBubble } from './MessageBubble';

/** 高亮保持的时间。 */
export const HIGHLIGHT_MS = 2500;

export function Feed({
  items, all, dropPeer, dropText, highlight, onHighlightDone, empty,
}: {
  items: TimelineItem[];
  all: boolean;
  dropPeer: string; // 拖放落在消息流上时发给谁（data-peer，见 store/drop.ts）
  dropText: string; // 遮罩上的文字
  highlight: number | null; // 要高亮的历史编号
  onHighlightDone: () => void;
  empty: ReactNode; // 没有消息时显示的内容
}) {
  const entries = useMemo(() => groupFeed(items, all), [items, all]);
  const end = useRef<HTMLDivElement>(null);
  const box = useRef<HTMLDivElement>(null);

  // 有新条目时滚到底部；有要高亮的条目时滚到它。副作用函数不能返回 scrollIntoView 的结果：较新的 Chromium 里
  // 它返回 Promise，React 会把返回值当成清理函数。用 ref 记住上一次的 last：只有真的来了新条目才滚到底部，
  // 否则高亮过期（target 从编号变回 null）也会触发这个 effect，把用户从刚跳转到的搜索结果拉走。
  const last = items.length > 0 ? items[items.length - 1].key : '';
  const target = highlight !== null && items.some((i) => i.kind === 'message' && i.id === highlight) ? highlight : null;
  const seen = useRef('');
  useEffect(() => {
    const changed = seen.current !== last;
    seen.current = last;
    if (target !== null || !changed) return;
    end.current?.scrollIntoView?.({ block: 'end' });
  }, [last, target]);
  useEffect(() => {
    if (target === null) return;
    box.current?.querySelector(`[data-highlighted]`)?.scrollIntoView?.({ block: 'center' });
    const t = window.setTimeout(onHighlightDone, HIGHLIGHT_MS);
    return () => window.clearTimeout(t);
  }, [target, onHighlightDone]);

  return (
    // 外层是拖放目标（不滚动），遮罩盖满它；里层滚动。
    <Box
      data-file-drop-target
      data-peer={dropPeer}
      data-testid="feed"
      sx={{ flex: 1, minHeight: 0, position: 'relative', display: 'flex', '&.file-drop-target-active > .drop-overlay': { display: 'grid' } }}
    >
      <Box ref={box} sx={{ flex: 1, overflowY: 'auto', bgcolor: 'portide.feed', px: 2.5, py: 2, display: 'flex', flexDirection: 'column', gap: 1 }}>
        {entries.length === 0 && empty}
        {entries.map((e) =>
          e.type === 'day' ? (
            <Typography key={e.key} variant="caption" sx={{ alignSelf: 'center', px: 1.25, py: 0.25, borderRadius: '10px', bgcolor: 'portide.chip', color: 'portide.muted', my: 0.5 }}>
              {e.label}
            </Typography>
          ) : (
            <Box key={e.key} sx={{ display: 'flex', flexDirection: 'column', alignItems: e.item.direction === 'out' ? 'flex-end' : 'flex-start', mt: e.meta ? 0.75 : 0 }}>
              {e.meta && (
                <Typography variant="caption" sx={{ color: 'portide.muted', mb: 0.25, px: 0.5 }}>
                  {e.meta}
                </Typography>
              )}
              <MessageBubble item={e.item} highlighted={e.item.kind === 'message' && e.item.id === target} />
            </Box>
          ),
        )}
        <div ref={end} />
      </Box>
      <Box
        className="drop-overlay"
        role="presentation"
        sx={{
          display: 'none', position: 'absolute', inset: 8, placeItems: 'center', pointerEvents: 'none', borderRadius: '14px',
          border: 2, borderStyle: 'dashed', borderColor: 'portide.accent', bgcolor: 'rgb(79 70 229 / 0.08)', backdropFilter: 'blur(1px)',
        }}
      >
        <Box sx={{ bgcolor: 'portide.bg', borderRadius: '14px', px: 3.5, py: 2.25, textAlign: 'center', boxShadow: 2, color: 'portide.accent' }}>
          <Upload size={26} />
          <Typography sx={{ fontWeight: 600, mt: 0.5, color: 'portide.fg' }}>{dropText}</Typography>
        </Box>
      </Box>
    </Box>
  );
}
