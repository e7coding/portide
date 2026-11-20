// api/portide 的测试替身：每个 Go 方法都是 vi.fn()，测试里按需设置返回值。
// 用法：vi.mock('../api/portide', () => import('../test/portideMock'))，再从这里导入 Service / Platform 设置行为。
import { vi } from 'vitest';

const methods = [
  'State', 'Unlock', 'Init', 'Join', 'LeaveCircle', 'Retry', 'UseAutomaticPort',
  'Conversations', 'Timeline', 'Search', 'Thumbnail', 'SendText', 'SendFiles', 'SendPasted', 'CancelSend', 'DeleteHistory',
  'OpenFile', 'RevealFile', 'HistoryFiles', 'Inbox', 'DecideInbox',
  'Transfers', 'PauseSend', 'ResumeSend', 'RetrySend', 'PauseAll', 'ResumeAll', 'StopReceive',
  'NearbyInviters', 'Devices', 'StartPairing', 'PairingStatus', 'CancelPairing', 'RemoveDevice', 'RenameDevice', 'CanRename', 'SetDeviceIcon',
  'Settings', 'SetSaveDir', 'OpenSaveDir', 'SetReceivePolicy', 'SetHistoryDays', 'SetQueueDays', 'SetNotify', 'ClearHistory',
  'SetListenPort', 'LocalAddresses', 'Relay', 'AddRelay', 'RemoveRelay',
] as const;

type Mocked = Record<(typeof methods)[number], ReturnType<typeof vi.fn>>;

export const Service = Object.fromEntries(methods.map((m) => [m, vi.fn().mockResolvedValue(undefined)])) as Mocked;
const platformMethods = ['PickFiles', 'PickFolder', 'Autostart', 'SetAutostart', 'OpenURL', 'Prefs', 'SetCloseToTray', 'SetHideHint', 'SetLanguage', 'SetUpdateCheck', 'CheckUpdate'] as const;

/** 平台方法的默认返回值（"用户取消了对话框"、"没有开机启动"、默认偏好）。 */
const platformDefaults: Record<(typeof platformMethods)[number], unknown> = {
  PickFiles: [], PickFolder: '', Autostart: false, SetAutostart: undefined, OpenURL: undefined,
  Prefs: { closeToTray: true, hideHint: false, hasTray: true, language: '', uiLanguage: 'zh' }, SetCloseToTray: undefined, SetHideHint: undefined,
  SetLanguage: { closeToTray: true, hideHint: false, hasTray: true, language: 'en', uiLanguage: 'en' },
  SetUpdateCheck: undefined,
  CheckUpdate: { current: '0.5.0', latest: '', url: '', newer: false, checkedAt: '0001-01-01T00:00:00Z' },
};

export const Platform = Object.fromEntries(platformMethods.map((m) => [m, vi.fn().mockResolvedValue(platformDefaults[m])])) as Record<
  (typeof platformMethods)[number],
  ReturnType<typeof vi.fn>
>;

/** 每个测试开始前恢复成"什么都不做"。 */
export function resetPortideMock() {
  for (const fn of Object.values(Service)) fn.mockReset().mockResolvedValue(undefined);
  for (const m of platformMethods) Platform[m].mockReset().mockResolvedValue(platformDefaults[m]);
}
