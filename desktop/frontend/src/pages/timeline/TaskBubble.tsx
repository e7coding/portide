// 还没结束的发送（M3c 设计 4.1）：正在发送时有进度条，可以暂停 / 取消；排队时写明"等「手机」上线后送达"，可以撤回；
// 中断时写明稍后重试，也可以立即重试；暂停后可以继续。
import { Box, Button, LinearProgress, Typography } from '@mui/material';
import { Clock, Pause, Play, RotateCw, Undo2, X } from 'lucide-react';
import { Service, type TimelineItem } from '../../api/portide';
import { useRun } from '../../api/useRun';
import { FileTypeIcon } from '../../components/FileTypeIcon';
import { formatBytes, isTextSummary } from '../../format';
import { fileKind } from '../../icons';
import { t } from '../../i18n';

/** 任务的状态文字。 */
export function taskText(item: TimelineItem): string {
  if (item.paused) return t('已暂停');
  switch (item.status) {
    case 'sending':
      return item.bytesTotal > 0 ? t('正在发送 {pct}%', { pct: Math.floor((item.bytesDone * 100) / item.bytesTotal) }) : t('正在发送');
    case 'pending':
      return t('等「{name}」上线后送达', { name: item.peerName });
    case 'interrupted':
      return item.error ? t('中断，稍后重试（{error}）', { error: item.error }) : t('中断，稍后重试');
  }
  return item.status;
}

export function TaskBubble({ item }: { item: TimelineItem }) {
  const run = useRun();
  const sending = item.status === 'sending' && !item.paused;
  const isText = isTextSummary(item.summary);
  return (
    <Box sx={{ width: 300, maxWidth: '100%', borderRadius: '12px', border: 1, borderColor: 'portide.line', bgcolor: 'portide.bg', p: 1.25 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.25 }}>
        <FileTypeIcon kind={isText ? 'message' : fileKind(item.summary.replace(/（.*$/, ''))} size={36} />
        <Box sx={{ flex: 1, minWidth: 0 }}>
          <Typography noWrap title={item.summary} sx={{ fontWeight: 500 }}>
            {item.summary}
          </Typography>
          <Typography variant="caption" sx={{ color: 'portide.muted', display: 'flex', alignItems: 'center', gap: 0.5 }}>
            {!sending && <Clock size={12} />}
            {taskText(item)}
            {item.bytesTotal > 0 && ` · ${formatBytes(item.bytesDone)} / ${formatBytes(item.bytesTotal)}`}
          </Typography>
        </Box>
      </Box>
      {sending && (
        <LinearProgress
          variant={item.bytesTotal > 0 ? 'determinate' : 'indeterminate'}
          value={item.bytesTotal > 0 ? (item.bytesDone * 100) / item.bytesTotal : 0}
          sx={{ mt: 1 }}
        />
      )}
      <Box sx={{ display: 'flex', justifyContent: 'flex-end', gap: 0.5, mt: 0.75 }}>
        {item.status === 'interrupted' && !item.paused && (
          <Button size="small" startIcon={<RotateCw size={13} />} onClick={() => void run(() => Service.RetrySend(item.id), t('正在重试'))}>
            {t('立即重试')}
          </Button>
        )}
        {item.paused ? (
          <Button size="small" startIcon={<Play size={13} />} onClick={() => void run(() => Service.ResumeSend(item.id))}>
            {t('继续')}
          </Button>
        ) : (
          item.status !== 'pending' && (
            <Button size="small" startIcon={<Pause size={13} />} onClick={() => void run(() => Service.PauseSend(item.id))}>
              {t('暂停')}
            </Button>
          )
        )}
        <Button
          size="small"
          color="error"
          startIcon={item.status === 'pending' ? <Undo2 size={13} /> : <X size={13} />}
          onClick={() => void run(() => Service.CancelSend(item.id))}
        >
          {item.status === 'pending' ? t('撤回') : t('取消')}
        </Button>
      </Box>
    </Box>
  );
}
