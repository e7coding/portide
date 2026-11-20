import { groupFeed, MERGE_GAP_MS } from './grouping';
import { item } from '../../test/fixtures';

const now = new Date(2026, 9, 26, 18, 0).getTime();
const at = (h: number, m: number, day = 26) => new Date(2026, 9, day, h, m).getTime();

test('day separators, merged runs and the 5-minute rule', () => {
  const items = [
    item({ key: 'a', at: at(23, 50, 25) }),
    item({ key: 'b', at: at(9, 0) }),
    item({ key: 'c', at: at(9, 3) }), // 同一台设备 3 分钟内：合并
    item({ key: 'd', at: at(9, 3) + MERGE_GAP_MS + 1 }), // 超过 5 分钟：重新显示时间
    item({ key: 'e', at: at(9, 9), direction: 'out' }), // 方向变了
    item({ key: 'f', at: at(9, 10), direction: 'out' }),
  ];
  const feed = groupFeed(items, false, now);
  expect(feed.map((e) => (e.type === 'day' ? `[${e.label}]` : `${e.key}:${e.meta ?? '-'}`))).toEqual([
    '[昨天]', 'a:23:50', '[今天]', 'b:09:00', 'c:-', 'd:09:08', 'e:09:09', 'f:-',
  ]);
});

test('"all devices" names the device on each run', () => {
  const items = [
    item({ key: 'a', at: at(9, 0) }),
    item({ key: 'b', at: at(9, 1), peerKey: 'PHON', peerName: '手机' }), // 换了设备：重新显示
    item({ key: 'c', at: at(9, 2), peerKey: 'PHON', peerName: '手机', direction: 'out' }),
  ];
  const metas = groupFeed(items, true, now).flatMap((e) => (e.type === 'item' ? [e.meta] : []));
  expect(metas).toEqual(['台式机 · 09:00', '手机 · 09:01', '发给「手机」 · 09:02']);
});
