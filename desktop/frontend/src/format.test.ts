import { formatBytes, formatCountdown, formatEta, formatSpeed, formatTime, taskStatusText } from './format';

test('formatTime shows only the time for today', () => {
  const now = new Date(2026, 9, 24, 18, 0).getTime();
  expect(formatTime(new Date(2026, 9, 24, 9, 5).getTime(), now)).toBe('09:05');
  expect(formatTime(new Date(2026, 9, 23, 23, 59).getTime(), now)).toBe('10-23 23:59');
  expect(formatTime(0, now)).toBe('-');
});

test('formatBytes uses 1024 steps', () => {
  expect(formatBytes(512)).toBe('512 B');
  expect(formatBytes(1536)).toBe('1.5 KB');
  expect(formatBytes(100 * 1024 * 1024)).toBe('100 MB');
});

test('formatCountdown never goes negative', () => {
  expect(formatCountdown(299_500)).toBe('4:59');
  expect(formatCountdown(-5)).toBe('0:00');
});

test('formatSpeed and formatEta', () => {
  expect(formatSpeed(1536)).toBe('1.5 KB/s');
  expect(formatEta(-1)).toBe('');
  expect(formatEta(0)).toBe('约 1 秒');
  expect(formatEta(45)).toBe('约 45 秒');
  expect(formatEta(61)).toBe('约 2 分钟');
  expect(formatEta(3900)).toBe('约 1 小时 5 分');
});

test('taskStatusText puts paused first', () => {
  expect(taskStatusText('sending', false)).toBe('正在发送');
  expect(taskStatusText('sending', true)).toBe('已暂停');
  expect(taskStatusText('pending', false)).toBe('排队中，等对方上线');
});

test('day separators, list times and "ago"', async () => {
  const { formatAgo, formatDay, formatListTime } = await import('./format');
  const now = new Date(2026, 9, 26, 18, 0).getTime();
  expect(formatDay(new Date(2026, 9, 26, 0, 1).getTime(), now)).toBe('今天');
  expect(formatDay(new Date(2026, 9, 25, 23, 59).getTime(), now)).toBe('昨天');
  expect(formatDay(new Date(2026, 0, 3, 9, 0).getTime(), now)).toBe('1月3日');
  expect(formatDay(new Date(2025, 11, 31, 9, 0).getTime(), now)).toBe('2025年12月31日');
  expect(formatListTime(new Date(2026, 9, 26, 9, 5).getTime(), now)).toBe('09:05');
  expect(formatListTime(new Date(2026, 9, 25, 9, 5).getTime(), now)).toBe('昨天');
  expect(formatListTime(new Date(2026, 8, 1, 9, 5).getTime(), now)).toBe('09-01');
  expect(formatListTime(0, now)).toBe('');
  expect(formatAgo(now - 20_000, now)).toBe('刚刚');
  expect(formatAgo(now - 5 * 60_000, now)).toBe('5 分钟前');
  expect(formatAgo(now - 2 * 3600_000, now)).toBe('2 小时前');
  expect(formatAgo(now - 3 * 86400_000, now)).toBe('3 天前');
  expect(formatAgo(0, now)).toBe('从未在线');
});
