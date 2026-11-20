// 传输页（M3c 设计 4.2）：顶部汇总（正在传输 / 排队的数量，"全部暂停 / 全部继续"）；发送和接收两组，每条有类型图标、
// 名字、对方、连接方式、进度和"已传 / 总量 · 速度 · 剩余时间"；排队的写明原因和还会等多久；最后是最近完成的 5 条。
import { useState, type ReactNode } from 'react';
import {
  Box, Button, ButtonBase, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, IconButton, LinearProgress, Tooltip, Typography,
} from '@mui/material';
import { useQueryClient } from '@tanstack/react-query';
import { ArrowDownLeft, ArrowUpRight, Pause, Play, RotateCw, X } from 'lucide-react';
import { Service, type TimelineItem, type TransferItem } from '../api/portide';
import { useDevices, useSettings, useTimeline, useTransfers } from '../api/queries';
import { useRun } from '../api/useRun';
import { FileTypeIcon } from '../components/FileTypeIcon';
import { formatBytes, formatEta, formatListTime, formatSpeed, isTextSummary, summaryName } from '../format';
import { fileKind, MESSAGE_STATES, PATH_ICONS } from '../icons';
import { useNav } from '../store/nav';
import { folderTitle } from './timeline/FileCard';
import { messageState } from './timeline/MessageBubble';
import { t } from '../i18n';

/** 最近完成的显示几条。 */
export const RECENT = 5;
const DAY_MS = 24 * 3600 * 1000;

const moving = (it: TransferItem) => it.status === 'receiving' || (it.status === 'sending' && !it.paused);

/** 一条传输的状态行。queueDays 用来算排队的任务还会等多久。 */
export function transferStatus(it: TransferItem, queueDays: number, now = Date.now()): string {
  if (it.direction === 'in') return t('正在接收');
  if (it.paused) return t('已暂停');
  switch (it.status) {
    case 'sending':
      return t('正在发送');
    case 'pending': {
      const left = Math.ceil((it.at + queueDays * DAY_MS - now) / DAY_MS);
      return left > 0 ? t('排队中，等「{name}」上线 · 还会等 {n} 天', { name: it.peerName, n: left }) : t('排队中，等「{name}」上线', { name: it.peerName });
    }
    case 'interrupted':
      return it.error ? t('中断，稍后自动重试（{error}）', { error: it.error }) : t('中断，稍后自动重试');
  }
  return it.status;
}

/** 历史条目的标题：文字的第一行、文件名、文件夹名，或没收到文件时的报价摘要。 */
export function itemTitle(it: TimelineItem): string {
  if (it.texts.length > 0) return it.texts[0].split('\n')[0];
  if (it.fileCount === 1) return it.files[0].name.split('/').pop() ?? it.files[0].name;
  if (it.fileCount > 1) return folderTitle(it);
  return it.summary;
}

const kindOf = (summary: string) => (isTextSummary(summary) ? 'message' : fileKind(summaryName(summary)));

export function Transfers() {
  const qc = useQueryClient();
  const run = useRun();
  const nav = useNav();
  const { data } = useTransfers();
  const { data: devices = [] } = useDevices();
  const { data: settings } = useSettings();
  const { data: history = [] } = useTimeline('');
  const [stopping, setStopping] = useState<TransferItem | null>(null);
  const outgoing = data?.outgoing ?? [];
  const incoming = data?.incoming ?? [];
  const queued = outgoing.length + incoming.length - (data?.active ?? 0);
  const allPaused = outgoing.length > 0 && outgoing.every((it) => it.paused);
  const recent = history.filter((it) => it.kind === 'message').slice(-RECENT).reverse();
  const pathOf = (peerKey: string) => PATH_ICONS[devices.find((d) => d.keyId === peerKey && d.online)?.path ?? ''];

  const act = async (action: () => Promise<unknown>, success?: string) => {
    await run(action, success);
    void qc.invalidateQueries({ queryKey: ['transfers'] });
  };
  const stop = async () => {
    const it = stopping;
    setStopping(null);
    if (it) await act(() => Service.StopReceive(it.peerKey, it.transferId));
  };

  const row = (it: TransferItem) => {
    const path = pathOf(it.peerKey);
    const parts = [transferStatus(it, settings?.queueDays ?? 7)];
    if (it.bytesTotal > 0) parts.push(`${formatBytes(it.bytesDone)} / ${formatBytes(it.bytesTotal)}`);
    if (moving(it) && it.bytesPerSec > 0) parts.push(formatSpeed(it.bytesPerSec));
    if (moving(it) && it.etaSec >= 0) parts.push(t('剩余{eta}', { eta: formatEta(it.etaSec) }));
    return (
      <Box key={it.key} data-testid={`transfer-${it.key}`} sx={{ display: 'flex', alignItems: 'center', gap: 1.5, px: 1.75, py: 1.5, borderRadius: '12px', border: 1, borderColor: 'portide.line', bgcolor: 'portide.bg' }}>
        <FileTypeIcon kind={kindOf(it.summary)} size={38} />
        <Box sx={{ flex: 1, minWidth: 0 }}>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
            <Typography noWrap title={it.summary} sx={{ fontWeight: 500, minWidth: 0 }}>
              {it.summary}
            </Typography>
            {path && (
              <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5, px: 0.75, height: 20, borderRadius: '6px', bgcolor: 'portide.chip', color: 'portide.muted', fontSize: 11, flexShrink: 0 }}>
                <path.icon size={11} />
                {path.label}
              </Box>
            )}
          </Box>
          <Typography variant="caption" sx={{ color: 'portide.muted', display: 'block' }}>
            {it.direction === 'out' ? t('发给「{name}」', { name: it.peerName }) : t('来自「{name}」', { name: it.peerName })}
          </Typography>
          {moving(it) && (
            <LinearProgress
              variant={it.bytesTotal > 0 ? 'determinate' : 'indeterminate'}
              value={it.bytesTotal > 0 ? (it.bytesDone * 100) / it.bytesTotal : 0}
              sx={{ my: 0.75 }}
            />
          )}
          <Typography variant="caption" sx={{ color: it.status === 'interrupted' && !it.paused ? 'portide.warn' : 'portide.muted' }}>
            {parts.join(' · ')}
          </Typography>
        </Box>
        <Box sx={{ display: 'flex', gap: 0.5, flexShrink: 0 }}>
          {it.direction === 'out' ? (
            <>
              {it.status === 'interrupted' && !it.paused && (
                <IconTip label={t('立即重试')} onClick={() => void act(() => Service.RetrySend(it.id), t('正在重试'))}>
                  <RotateCw size={16} />
                </IconTip>
              )}
              {it.paused ? (
                <IconTip label={t('继续')} onClick={() => void act(() => Service.ResumeSend(it.id))}>
                  <Play size={16} />
                </IconTip>
              ) : (
                <IconTip label={t('暂停')} onClick={() => void act(() => Service.PauseSend(it.id))}>
                  <Pause size={16} />
                </IconTip>
              )}
              <IconTip label={t('取消')} danger onClick={() => void act(() => Service.CancelSend(it.id))}>
                <X size={16} />
              </IconTip>
            </>
          ) : (
            <IconTip label={t('停止接收')} danger onClick={() => setStopping(it)}>
              <X size={16} />
            </IconTip>
          )}
        </Box>
      </Box>
    );
  };

  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, px: 3, py: 1.5, borderBottom: 1, borderColor: 'portide.line', minHeight: 60 }}>
        <Typography variant="h3" component="h1" sx={{ flex: 1 }}>
          {t('传输')}
        </Typography>
        <Stat value={data?.active ?? 0} label={t('个正在传输')} accent />
        <Stat value={queued} label={t('个等待中')} />
        <Button
          size="small"
          variant="tonal"
          disabled={outgoing.length === 0}
          startIcon={allPaused ? <Play size={14} /> : <Pause size={14} />}
          onClick={() => void act(() => (allPaused ? Service.ResumeAll() : Service.PauseAll()), allPaused ? t('已全部继续') : t('已全部暂停'))}
        >
          {allPaused ? t('全部继续') : t('全部暂停')}
        </Button>
      </Box>
      <Box sx={{ flex: 1, overflowY: 'auto', px: 3, py: 2.5, bgcolor: 'portide.feed' }}>
        {outgoing.length === 0 && incoming.length === 0 && (
          <Typography sx={{ color: 'portide.muted', textAlign: 'center', my: 6 }}>{t('没有进行中的传输')}</Typography>
        )}
        {outgoing.length > 0 && (
          <Section title={t('发送')} icon={<ArrowUpRight size={15} />}>
            {outgoing.map(row)}
          </Section>
        )}
        {incoming.length > 0 && (
          <Section title={t('接收')} icon={<ArrowDownLeft size={15} />}>
            {incoming.map(row)}
          </Section>
        )}
        {recent.length > 0 && (
          <Section title={t('最近完成')}>
            {recent.map((it) => {
              const st = messageState(it);
              const Icon = st && st.state !== 'cancelled' ? MESSAGE_STATES[st.state].icon : null;
              const bad = it.status === 'failed' || it.status === 'partial' || it.status === 'cancelled';
              return (
                <ButtonBase
                  key={it.key}
                  onClick={() => nav.openConversation(it.peerKey, it.id)}
                  aria-label={t('{title}，在时间线中查看', { title: itemTitle(it) })}
                  sx={{ display: 'flex', width: '100%', justifyContent: 'flex-start', gap: 1.25, px: 1.5, py: 1, borderRadius: '10px', textAlign: 'left', '&:hover': { bgcolor: 'portide.chip' } }}
                >
                  <FileTypeIcon kind={it.texts.length > 0 ? 'message' : it.fileCount > 1 ? 'folder' : fileKind(itemTitle(it))} size={30} />
                  <Box sx={{ flex: 1, minWidth: 0 }}>
                    <Typography noWrap sx={{ fontSize: 13.5 }}>
                      {itemTitle(it)}
                    </Typography>
                    <Typography variant="caption" sx={{ color: bad ? 'portide.err' : 'portide.muted', display: 'flex', alignItems: 'center', gap: 0.5 }}>
                      {it.direction === 'out' ? t('发给「{name}」', { name: it.peerName }) : t('来自「{name}」', { name: it.peerName })}
                      {Icon && <Icon size={12} />}
                      {bad && st ? ` · ${st.text}` : ''}
                    </Typography>
                  </Box>
                  <Typography variant="caption" sx={{ color: 'portide.muted' }}>
                    {formatListTime(it.at)}
                  </Typography>
                </ButtonBase>
              );
            })}
          </Section>
        )}
      </Box>
      <Dialog open={stopping !== null} onClose={() => setStopping(null)}>
        <DialogTitle>{t('停止接收？')}</DialogTitle>
        <DialogContent>
          <DialogContentText>{t('对方会收到"停止了接收"，已收到的部分会删除。')}</DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setStopping(null)}>{t('继续接收')}</Button>
          <Button color="error" variant="contained" onClick={() => void stop()}>
            {t('停止接收')}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}

function Stat({ value, label, accent = false }: { value: number; label: string; accent?: boolean }) {
  return (
    <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 0.5, px: 1.25, py: 0.5, borderRadius: '9px', bgcolor: accent && value > 0 ? 'portide.accentSoft' : 'portide.chip' }}>
      <Typography sx={{ fontWeight: 700, color: accent && value > 0 ? 'portide.accent' : 'portide.fg' }}>{value}</Typography>
      <Typography variant="caption" sx={{ color: 'portide.muted' }}>
        {label}
      </Typography>
    </Box>
  );
}

function Section({ title, icon, children }: { title: string; icon?: ReactNode; children: ReactNode }) {
  return (
    <Box component="section" aria-label={title} sx={{ mb: 3 }}>
      <Typography variant="subtitle2" component="h2" sx={{ display: 'flex', alignItems: 'center', gap: 0.75, mb: 1, color: 'portide.fg2' }}>
        {icon}
        {title}
      </Typography>
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1 }}>{children}</Box>
    </Box>
  );
}

function IconTip({ label, onClick, danger = false, children }: { label: string; onClick: () => void; danger?: boolean; children: ReactNode }) {
  return (
    <Tooltip title={label}>
      <IconButton aria-label={label} onClick={onClick} sx={{ bgcolor: 'portide.chip', ...(danger && { '&:hover': { bgcolor: 'portide.errSoft', color: 'portide.err' } }) }}>
        {children}
      </IconButton>
    </Tooltip>
  );
}
