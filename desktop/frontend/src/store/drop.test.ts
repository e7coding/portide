import { ALL_DROP, dropTargets, parseDrop } from './drop';
import { saveTargets } from './targets';

const dev = (keyId: string, self = false) => ({ keyId, name: keyId, icon: 'laptop', self, online: true, path: 'lan', addr: '', lastSeen: 0, joinedAt: 0, move: '', moveUntil: 0 });
const devices = [dev('SELF', true), dev('DESK'), dev('PHON')];

beforeEach(() => localStorage.clear());

test('parses the drop payload and the old path list', () => {
  expect(parseDrop({ paths: ['/a', 3], peer: 'DESK' })).toEqual({ paths: ['/a'], peer: 'DESK' });
  expect(parseDrop(['/a', '/b'])).toEqual({ paths: ['/a', '/b'], peer: '' });
  expect(parseDrop(null)).toEqual({ paths: [], peer: '' });
});

test('a drop on a device row goes to that device, otherwise to the open conversation', () => {
  expect(dropTargets({ paths: ['/a'], peer: 'PHON' }, 'DESK', devices)).toEqual(['PHON']);
  expect(dropTargets({ paths: ['/a'], peer: '' }, 'DESK', devices)).toEqual(['DESK']);
  expect(dropTargets({ paths: ['/a'], peer: 'GONE' }, 'DESK', devices)).toEqual([]);
  expect(dropTargets({ paths: ['/a'], peer: '' }, 'SELF', devices)).toEqual([]);
});

test('in "all devices" (or on its row) the composer selection decides', () => {
  expect(dropTargets({ paths: ['/a'], peer: '' }, '', devices)).toEqual([]); // 两台以上、没选过：不猜
  saveTargets(['PHON', 'DESK']);
  expect(dropTargets({ paths: ['/a'], peer: ALL_DROP }, 'DESK', devices)).toEqual(['PHON', 'DESK']);
  expect(dropTargets({ paths: ['/a'], peer: '' }, '', devices)).toEqual(['PHON', 'DESK']);
});
