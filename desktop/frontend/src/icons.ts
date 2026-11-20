// 图标规则（M3c 设计 3）：设备类型、文件类型、连接方式和消息状态各有固定的 lucide 图标和颜色。
// iOS 按 docs/界面规范.md 里的同一张表对齐。
import {
  Check, CheckCheck, CircleAlert, Clock, Cloud, File, FileArchive, FileAudio, FileCode, FileImage, FileSpreadsheet,
  FileText, FileVideo, Folder, Laptop, MessageSquare, Monitor, Smartphone, Tablet, Wifi, Zap, type LucideIcon,
} from 'lucide-react';
import { t } from './i18n';

// ---- 设备 ----

export type DeviceIconName = 'laptop' | 'monitor' | 'smartphone' | 'tablet';

export const DEVICE_ICONS: Record<DeviceIconName, { icon: LucideIcon; label: string; color: string }> = {
  laptop: { icon: Laptop, get label() { return t('笔记本'); }, color: '#4F46E5' },
  monitor: { icon: Monitor, get label() { return t('台式机'); }, color: '#0891B2' },
  smartphone: { icon: Smartphone, get label() { return t('手机'); }, color: '#DB2777' },
  tablet: { icon: Tablet, get label() { return t('平板'); }, color: '#059669' },
};

/** 服务层给的图标名（Device.icon）→ 图标；不认识的当作台式机。 */
export function deviceIcon(name: string) {
  return DEVICE_ICONS[name as DeviceIconName] ?? DEVICE_ICONS.monitor;
}

// ---- 连接方式 ----

export const PATH_ICONS: Record<string, { icon: LucideIcon; label: string }> = {
  lan: { icon: Wifi, get label() { return t('局域网'); } },
  direct: { icon: Zap, get label() { return t('直连'); } },
  relay: { icon: Cloud, get label() { return t('经中继'); } },
};

// ---- 文件类型 ----

export type FileKind = 'doc' | 'image' | 'video' | 'audio' | 'archive' | 'sheet' | 'code' | 'text' | 'folder' | 'message' | 'other';

export const FILE_KINDS: Record<FileKind, { icon: LucideIcon; color: string; label: string }> = {
  doc: { icon: FileText, color: '#DC2626', get label() { return t('文档'); } },
  image: { icon: FileImage, color: '#16A34A', get label() { return t('图片'); } },
  video: { icon: FileVideo, color: '#9333EA', get label() { return t('视频'); } },
  audio: { icon: FileAudio, color: '#EA580C', get label() { return t('音频'); } },
  archive: { icon: FileArchive, color: '#D97706', get label() { return t('压缩包'); } },
  sheet: { icon: FileSpreadsheet, color: '#059669', get label() { return t('表格'); } },
  code: { icon: FileCode, color: '#475569', get label() { return t('代码'); } },
  text: { icon: FileText, color: '#475569', get label() { return t('文本'); } },
  folder: { icon: Folder, color: '#D97706', get label() { return t('文件夹'); } },
  message: { icon: MessageSquare, color: '#4F46E5', get label() { return t('文字'); } },
  other: { icon: File, color: '#64748B', get label() { return t('文件'); } },
};

const EXTENSIONS: Record<string, FileKind> = {};
function register(kind: FileKind, exts: string) {
  for (const e of exts.split(' ')) EXTENSIONS[e] = kind;
}
register('doc', 'pdf doc docx ppt pptx key pages rtf odt odp epub wps');
register('image', 'jpg jpeg png gif webp heic heif bmp svg tif tiff ico avif raw dng');
register('video', 'mp4 mov mkv avi webm m4v wmv flv 3gp');
register('audio', 'mp3 wav flac aac m4a ogg opus wma amr');
register('archive', 'zip rar 7z tar gz tgz bz2 xz zst dmg iso apk ipa deb rpm');
register('sheet', 'xls xlsx csv tsv numbers ods et');
register('code', 'js mjs ts tsx jsx go py rs java kt swift c h cc cpp hpp cs rb php sh ps1 bat json yaml yml toml xml html css scss sql proto');
register('text', 'txt md log ini conf cfg');

/** 文件名（可以带目录）→ 文件类型，按扩展名判断，不区分大小写。 */
export function fileKind(name: string): FileKind {
  const base = name.split('/').pop() ?? name;
  const dot = base.lastIndexOf('.');
  if (dot <= 0) return 'other';
  return EXTENSIONS[base.slice(dot + 1).toLowerCase()] ?? 'other';
}

/** 能请求缩略图的文件（服务层只给 JPEG、PNG、GIF 生成缩略图）。 */
export function hasThumbnail(name: string): boolean {
  return /\.(jpe?g|png|gif)$/i.test(name);
}

// ---- 消息状态（发出的消息）----

export type MessageState = 'delivered' | 'partial' | 'sending' | 'queued' | 'failed';

export const MESSAGE_STATES: Record<MessageState, { icon: LucideIcon; label: string }> = {
  delivered: { icon: CheckCheck, get label() { return t('已送达'); } },
  partial: { icon: Check, get label() { return t('部分送达'); } },
  sending: { icon: Clock, get label() { return t('正在发送'); } },
  queued: { icon: Clock, get label() { return t('排队中'); } },
  failed: { icon: CircleAlert, get label() { return t('失败'); } },
};
