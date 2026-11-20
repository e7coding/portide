// 显示格式：时间、大小、剩余时间、速度、发送任务的状态。按当前界面语言输出。
import { lang, t } from './i18n';

const pad = (n: number) => String(n).padStart(2, '0');

/** Unix 毫秒 → "10-24 14:03"；今天的只显示 "14:03"；0 显示 "-"。 */
export function formatTime(ms: number, now = Date.now()): string {
  if (!ms) return '-';
  const d = new Date(ms);
  const today = new Date(now);
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  if (d.toDateString() === today.toDateString()) return hm;
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${hm}`;
}

/** 字节数 → "1.5 MB"（按 1024 进位，和命令行一致）。 */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ['KB', 'MB', 'GB', 'TB'];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v >= 100 ? v.toFixed(0) : v.toFixed(1)} ${units[i]}`;
}

/** 剩余毫秒 → "4:59"；已过期显示 "0:00"。 */
export function formatCountdown(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${pad(s % 60)}`;
}

/** 字节/秒 → "1.5 MB/s"。 */
export function formatSpeed(bytesPerSec: number): string {
  return `${formatBytes(bytesPerSec)}/s`;
}

/** 传输剩余秒数 → "约 45 秒" / "约 3 分钟" / "约 1 小时 5 分"；算不出来（负数）时为空。 */
export function formatEta(sec: number): string {
  if (sec < 0) return '';
  if (sec < 60) return t('约 {n} 秒', { n: Math.max(1, sec) });
  const min = Math.ceil(sec / 60);
  if (min < 60) return t('约 {n} 分钟', { n: min });
  return t('约 {h} 小时 {m} 分', { h: Math.floor(min / 60), m: min % 60 });
}

/** 未结束的发送任务的状态文字（时间线和传输页共用）。 */
export function taskStatusText(status: string, paused: boolean): string {
  if (paused) return t('已暂停');
  switch (status) {
    case 'pending':
      return t('排队中，等对方上线');
    case 'sending':
      return t('正在发送');
    case 'interrupted':
      return t('中断，稍后重试');
  }
  return status;
}

const MONTHS = ['Jan', 'Feb', 'Mar', 'Apr', 'May', 'Jun', 'Jul', 'Aug', 'Sep', 'Oct', 'Nov', 'Dec'];

const startOfDay = (ms: number) => new Date(new Date(ms).toDateString()).getTime();
const DAY_MS = 24 * 3600 * 1000;

/** 消息流的日期分隔：今天 / 昨天 / 10月24日（今年）/ 2025年10月24日；英文是 Today / Yesterday / Oct 24 / Oct 24, 2025。 */
export function formatDay(ms: number, now = Date.now()): string {
  const days = Math.round((startOfDay(now) - startOfDay(ms)) / DAY_MS);
  if (days === 0) return t('今天');
  if (days === 1) return t('昨天');
  const d = new Date(ms);
  const md = formatMonthDay(ms);
  if (d.getFullYear() === new Date(now).getFullYear()) return md;
  return lang() === 'en' ? `${md}, ${d.getFullYear()}` : `${d.getFullYear()}年${md}`; // i18n-ignore
}

/** 月日："10月24日" / "Oct 24"。 */
export function formatMonthDay(ms: number): string {
  const d = new Date(ms);
  return lang() === 'en' ? `${MONTHS[d.getMonth()]} ${d.getDate()}` : `${d.getMonth() + 1}月${d.getDate()}日`; // i18n-ignore
}

/** 时:分，例如 "09:05"。 */
export function formatClock(ms: number): string {
  const d = new Date(ms);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** 会话列表右侧的时间：今天显示时:分，昨天显示"昨天"，更早显示"10-24"；0 显示空。 */
export function formatListTime(ms: number, now = Date.now()): string {
  if (!ms) return '';
  const days = Math.round((startOfDay(now) - startOfDay(ms)) / DAY_MS);
  if (days === 0) return formatClock(ms);
  if (days === 1) return t('昨天');
  const d = new Date(ms);
  return `${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

/** 多久以前："刚刚" / "5 分钟前" / "2 小时前" / "3 天前" / "10-24"；0 表示从没在线过。 */
export function formatAgo(ms: number, now = Date.now()): string {
  if (!ms) return t('从未在线');
  const min = Math.floor((now - ms) / 60000);
  if (min < 1) return t('刚刚');
  if (min < 60) return t('{n} 分钟前', { n: min });
  if (min < 24 * 60) return t('{n} 小时前', { n: Math.floor(min / 60) });
  if (min < 30 * 24 * 60) return t('{n} 天前', { n: Math.floor(min / (24 * 60)) });
  return formatTime(ms, now);
}

/** 发送任务的摘要是不是一段文字（Go 端按发送时的语言生成："文本「…」" 或 'Text "…"'）。 */
export function isTextSummary(summary: string): boolean {
  return summary.startsWith('文本「') || summary.startsWith('Text “'); // i18n-ignore
}

/** 摘要里的第一个名字：去掉后面括号里的大小和数量（"报告.pdf（1.2 MB）"、"report.pdf (1.2 MB)"）。 */
export function summaryName(summary: string): string {
  return summary.replace(/(（| \().*$/, ''); // i18n-ignore
}

/** 不能改名时的提示：列出还没升级的设备。 */
export function renameBlockedText(missing: string[]): string {
  return t('{names}需要先升级到新版本，才能改名', { names: quoteNames(missing) });
}

/** 一串设备名：中文是「甲」「乙」，英文是 A, B。 */
export function quoteNames(names: string[]): string {
  return lang() === 'en' ? names.join(', ') : names.map((n) => `「${n}」`).join(''); // i18n-ignore
}
