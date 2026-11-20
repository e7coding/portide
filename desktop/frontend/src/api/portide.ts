// 界面访问 Go 端的唯一入口：转发 Wails 生成的绑定（frontend/bindings，由 wails3 generate bindings 生成，不手改）。
// 页面和组件只从这里导入，测试里用 vi.mock('…/api/portide') 整体替换。
export { Service } from '../../bindings/portide/app';
export { Platform } from '../../bindings/portide/desktop';
export type {
  Conversation,
  Device,
  InboxItem,
  JoinResult,
  Pairing,
  RelayEntry,
  RelayInfo,
  RemoveResult,
  RenameCheck,
  SearchResult,
  SendResult,
  Settings,
  State,
  TimelineFile,
  TimelineItem,
  TransferItem,
  TransferList,
} from '../../bindings/portide/app';
export type { DesktopPrefs } from '../../bindings/portide/desktop';
export type { Info as UpdateInfo } from '../../bindings/portide/core/update';
