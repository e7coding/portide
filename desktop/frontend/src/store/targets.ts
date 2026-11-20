// 发送区选中的目标设备（M3a 设计 3.1）：记在本地，发送区和拖拽发送（M3b 设计 3.3）共用。
import type { Device } from '../api/portide';

const TARGETS_KEY = 'portide.targets';

export function loadTargets(): string[] {
  try {
    const v = JSON.parse(localStorage.getItem(TARGETS_KEY) ?? '[]');
    return Array.isArray(v) ? v.filter((x) => typeof x === 'string') : [];
  } catch {
    return [];
  }
}

export function saveTargets(keys: string[]) {
  localStorage.setItem(TARGETS_KEY, JSON.stringify(keys));
}

/** 实际要发给的设备：只保留仍在设备圈里的；什么都没选、而且只有一台其他设备时默认选它。 */
export function chooseTargets(devices: Device[], stored: string[]): string[] {
  const others = devices.filter((d) => !d.self);
  const valid = stored.filter((k) => others.some((d) => d.keyId === k));
  return valid.length > 0 ? valid : others.length === 1 ? [others[0].keyId] : [];
}
