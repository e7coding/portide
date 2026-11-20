// 测试用的数据：字段齐全的设备、时间线条目、会话、传输条目，测试里只覆盖关心的字段。
import type { Conversation, Device, TimelineItem, TransferItem } from '../api/portide';

export const device = (over: Partial<Device> = {}): Device => ({
  keyId: 'DESK', name: '台式机', icon: 'monitor', self: false, online: true, path: 'lan', addr: '192.168.1.5:7443', lastSeen: 1, joinedAt: 1, move: '', moveUntil: 0, ...over,
});

export const self = device({ keyId: 'SELF', name: '笔记本', icon: 'laptop', self: true, online: false, path: '', addr: '', lastSeen: 0 });

export const item = (over: Partial<TimelineItem> = {}): TimelineItem => ({
  key: 'm1', kind: 'message', id: 1, direction: 'in', peerKey: 'DESK', peerName: '台式机', status: 'done', paused: false,
  texts: [], files: [], fileCount: 0, summary: '', bytesDone: 0, bytesTotal: 0, error: '', at: Date.now(), ...over,
});

export const conversation = (over: Partial<Conversation> = {}): Conversation => ({
  key: 'DESK', name: '台式机', icon: 'monitor', online: true, path: 'lan', lastPreview: '', lastDirection: '', lastAt: 0, queued: 0, ...over,
});

export const transfer = (over: Partial<TransferItem> = {}): TransferItem => ({
  key: 'o1', direction: 'out', id: 1, peerKey: 'DESK', peerName: '台式机', transferId: '', summary: '', status: 'pending', paused: false,
  bytesDone: 0, bytesTotal: 0, bytesPerSec: 0, etaSec: -1, error: '', at: 1, ...over,
});
