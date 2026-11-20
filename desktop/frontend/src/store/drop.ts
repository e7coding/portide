// 拖放发送的目标（M3c 设计 1、4.1）：文件拖到左侧某台设备上就发给它；拖到对话区（或窗口其他地方）发给当前会话的设备；
// 在"全部设备"会话里发给发送区选中的设备。托盘的"发送文件…"也走这里（没有落点）。
import type { Device } from '../api/portide';
import { chooseTargets, loadTargets } from './targets';

/** Go 端 portide:files-dropped 事件的内容：peer 是落点元素的 data-peer 属性（没有落点时为空）。 */
export interface DropPayload {
  paths: string[];
  peer: string;
}

/** 兼容两种事件内容：{paths, peer} 或旧的路径数组。 */
export function parseDrop(data: unknown): DropPayload {
  if (Array.isArray(data)) return { paths: data.filter((p) => typeof p === 'string'), peer: '' };
  if (data && typeof data === 'object') {
    const d = data as { paths?: unknown; peer?: unknown };
    return { paths: Array.isArray(d.paths) ? d.paths.filter((p): p is string => typeof p === 'string') : [], peer: typeof d.peer === 'string' ? d.peer : '' };
  }
  return { paths: [], peer: '' };
}

/** "全部设备"那一行的 data-peer：落在它上面时发给发送区选中的设备。 */
export const ALL_DROP = '*';

/** 决定发给哪些设备：落在某台设备上 → 它；否则在某台设备的会话里 → 它；否则发送区选中的设备。 */
export function dropTargets(drop: DropPayload, conversation: string, devices: Device[]): string[] {
  const others = devices.filter((d) => !d.self).map((d) => d.keyId);
  const key = drop.peer === ALL_DROP ? '' : drop.peer || conversation;
  if (key) return others.includes(key) ? [key] : [];
  return chooseTargets(devices, loadTargets());
}
