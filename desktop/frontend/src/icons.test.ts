import { deviceIcon, DEVICE_ICONS, fileKind, hasThumbnail } from './icons';

test('file kinds by extension, case-insensitive, ignoring folders', () => {
  const cases: Record<string, string> = {
    '季度报告.PDF': 'doc', '照片/IMG_0001.HEIC': 'image', 'a.mp4': 'video', '录音.m4a': 'audio', 'backup.tar.gz': 'archive',
    '预算.xlsx': 'sheet', 'main.go': 'code', '说明.md': 'text', 'Makefile': 'other', '.bashrc': 'other', 'x.unknown': 'other',
  };
  for (const [name, kind] of Object.entries(cases)) expect([name, fileKind(name)]).toEqual([name, kind]);
});

test('thumbnails only for JPEG, PNG and GIF', () => {
  expect(['a.jpg', 'b.JPEG', 'c.png', 'd.gif', 'e.heic', 'f.webp', 'g.mp4'].map(hasThumbnail)).toEqual([true, true, true, true, false, false, false]);
});

test('unknown device icons fall back to a monitor', () => {
  expect(deviceIcon('smartphone')).toBe(DEVICE_ICONS.smartphone);
  expect(deviceIcon('toaster')).toBe(DEVICE_ICONS.monitor);
});
