// TanStack Query 的查询 hook。查询键的第一段与 changes.ts 的 TOPIC_QUERIES 对应。
import { useQuery } from '@tanstack/react-query';
import { Platform, Service } from './portide';

/** 时间线一次最多显示的历史条数（更早的在命令行 portide history 里看）。 */
export const TIMELINE_LIMIT = 200;

export const usePortideState = () => useQuery({ queryKey: ['state'], queryFn: () => Service.State() });
export const useDevices = () => useQuery({ queryKey: ['devices'], queryFn: () => Service.Devices() });
export const useConversations = () => useQuery({ queryKey: ['conversations'], queryFn: () => Service.Conversations() });
export const useTimeline = (peerKey: string) =>
  useQuery({ queryKey: ['timeline', peerKey], queryFn: () => Service.Timeline(peerKey, TIMELINE_LIMIT) });
export const useTransfers = () => useQuery({ queryKey: ['transfers'], queryFn: () => Service.Transfers() });
export const useInbox = () => useQuery({ queryKey: ['inbox'], queryFn: () => Service.Inbox() });
export const useSettings = () => useQuery({ queryKey: ['settings'], queryFn: () => Service.Settings() });
export const useRelay = () => useQuery({ queryKey: ['relay'], queryFn: () => Service.Relay() });
export const useRenameCheck = () => useQuery({ queryKey: ['devices', 'rename'], queryFn: () => Service.CanRename() });
export const useLocalAddresses = () => useQuery({ queryKey: ['settings', 'addresses'], queryFn: () => Service.LocalAddresses() });
export const useAutostart = () => useQuery({ queryKey: ['autostart'], queryFn: () => Platform.Autostart() });
export const usePrefs = () => useQuery({ queryKey: ['prefs'], queryFn: () => Platform.Prefs() });
/** 有没有新版本：Go 端一天最多联网查一次（设置里关掉时只返回上次的结果），这里反复取也不会多查。 */
export const useUpdate = () => useQuery({ queryKey: ['update'], queryFn: () => Platform.CheckUpdate(false), retry: false });
export const useSearch = (query: string) =>
  useQuery({ queryKey: ['search', query], queryFn: () => Service.Search(query), enabled: query.trim() !== '' });
/** 缩略图不随变化信号刷新（文件变了键里的编号也不会变，但服务层按文件修改时间缓存，重开窗口即更新）。 */
export const useThumbnail = (historyId: number, index: number, enabled: boolean) =>
  useQuery({ queryKey: ['thumb', historyId, index], queryFn: () => Service.Thumbnail(historyId, index), enabled, staleTime: Infinity });
