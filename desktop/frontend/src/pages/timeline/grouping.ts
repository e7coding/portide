// 消息流的分组（M3c 设计 4.1）：按日期插入分隔；同一台设备、同一方向的连续消息合并，
// 间隔不超过 5 分钟的不再重复显示名字和时间。
import type { TimelineItem } from '../../api/portide';
import { formatClock, formatDay } from '../../format';
import { t } from '../../i18n';

/** 连续消息之间超过这么久才重新显示时间。 */
export const MERGE_GAP_MS = 5 * 60 * 1000;

export type FeedEntry =
  | { type: 'day'; key: string; label: string }
  | { type: 'item'; key: string; item: TimelineItem; meta: string | null }; // meta：消息上方的一行（名字和时间），合并时为空

/** all 为真时（"全部设备"会话）meta 带上设备名：收到的写"台式机"，发出的写"发给「手机」"。 */
export function groupFeed(items: TimelineItem[], all: boolean, now = Date.now()): FeedEntry[] {
  const out: FeedEntry[] = [];
  let day = '';
  let prev: TimelineItem | null = null;
  for (const item of items) {
    const label = formatDay(item.at, now);
    let startsRun = false;
    if (label !== day) {
      out.push({ type: 'day', key: `day-${item.key}`, label });
      day = label;
      startsRun = true;
    }
    if (!prev || prev.direction !== item.direction || prev.peerKey !== item.peerKey || item.at - prev.at > MERGE_GAP_MS) startsRun = true;
    let meta: string | null = null;
    if (startsRun) {
      const who = all ? (item.direction === 'out' ? t('发给「{name}」', { name: item.peerName }) : item.peerName) : '';
      meta = who ? `${who} · ${formatClock(item.at)}` : formatClock(item.at);
    }
    out.push({ type: 'item', key: item.key, item, meta });
    prev = item;
  }
  return out;
}
