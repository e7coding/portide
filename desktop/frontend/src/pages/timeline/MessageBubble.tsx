// 消息流里的一条（M3c 设计 4.1）：收到的在左（白气泡），发出的在右（靛蓝气泡）；文字里的链接可以点；
// 文件和文件夹是卡片；发出的消息下面是状态（已送达 / 部分送达 / 失败 + 重发 / 已取消）。
// 右键（或悬停时的"更多"按钮）：复制、打开、在文件夹中显示、删除这条记录。
import { useState, type MouseEvent, type ReactNode } from 'react';
import { Box, Button, IconButton, Link, ListItemIcon, Menu, MenuItem, Tooltip, Typography } from '@mui/material';
import { Copy, Ellipsis, ExternalLink, FolderOpen, RotateCw, Trash2 } from 'lucide-react';
import { Clipboard } from '@wailsio/runtime';
import { Platform, Service, type TimelineItem } from '../../api/portide';
import { useRun } from '../../api/useRun';
import { FileTypeIcon } from '../../components/FileTypeIcon';
import { fileKind, MESSAGE_STATES, type MessageState } from '../../icons';
import { FileCard, FolderCard } from './FileCard';
import { linkify } from './linkify';
import { TaskBubble } from './TaskBubble';
import { t } from '../../i18n';

/** 已经结束的消息在气泡下显示的状态；收到的、完整送达的只在发出时显示"已送达"。 */
export function messageState(item: TimelineItem): { state: MessageState | 'cancelled'; text: string } | null {
  const out = item.direction === 'out';
  switch (item.status) {
    case 'done':
      return out ? { state: 'delivered', text: MESSAGE_STATES.delivered.label } : null;
    case 'partial':
      return { state: 'partial', text: out ? t('部分文件没有送达') : t('部分文件没有收到') };
    case 'failed':
      return {
        state: 'failed',
        text: out
          ? item.error ? t('没有送达：{error}', { error: item.error }) : t('没有送达')
          : item.error ? t('接收失败：{error}', { error: item.error }) : t('接收失败'),
      };
    case 'cancelled':
      return { state: 'cancelled', text: item.error || t('已取消') };
  }
  return null;
}

/** 重发一条没有完全送达的消息：文字直接再发；文件用本机上的原文件（已被移走的跳过）。 */
async function resend(item: TimelineItem) {
  if (item.texts.length > 0) {
    for (const t of item.texts) await Service.SendText([item.peerKey], t);
  }
  if (item.fileCount > 0) {
    const paths = (await Service.HistoryFiles(item.id)).filter((p) => p !== '');
    if (paths.length === 0) throw new Error(t('原文件已被移走或删除，无法重发'));
    await Service.SendFiles([item.peerKey], paths);
  }
}

function TextBody({ text, out }: { text: string; out: boolean }) {
  const run = useRun();
  return (
    <Typography component="div" sx={{ whiteSpace: 'pre-wrap', wordBreak: 'break-word' }}>
      {linkify(text).map((p, i) =>
        p.url ? (
          <Link
            key={i}
            component="button"
            type="button"
            onClick={() => void run(() => Platform.OpenURL(p.url!))}
            sx={{ color: out ? 'inherit' : 'portide.accent', textDecorationColor: 'currentColor', font: 'inherit', verticalAlign: 'baseline', textAlign: 'left', wordBreak: 'break-all' }}
          >
            {p.text}
          </Link>
        ) : (
          <span key={i}>{p.text}</span>
        ),
      )}
    </Typography>
  );
}

export function MessageBubble({ item, highlighted = false }: { item: TimelineItem; highlighted?: boolean }) {
  const run = useRun();
  const out = item.direction === 'out';
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const openMenu = (e: MouseEvent) => {
    e.preventDefault();
    setMenu({ x: e.clientX, y: e.clientY });
  };
  if (item.kind === 'task') {
    return (
      <Row out highlighted={highlighted} testId={item.key}>
        <TaskBubble item={item} />
      </Row>
    );
  }

  const state = messageState(item);
  const Icon = state && state.state !== 'cancelled' ? MESSAGE_STATES[state.state].icon : null;
  const firstFile = item.files[0];
  // 复制走 Go 端的系统剪贴板：Linux 上页面从 wails:// 加载，WebKitGTK 不当它是安全上下文，没有 navigator.clipboard。
  const copy = () => void run(() => Clipboard.SetText(item.texts.join('\n')), t('已复制'));
  const close = (f?: () => void) => () => {
    setMenu(null);
    f?.();
  };
  return (
    <Row out={out} highlighted={highlighted} testId={item.key} onContextMenu={openMenu}>
      <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 0.5, flexDirection: out ? 'row-reverse' : 'row', '&:hover .msg-more': { opacity: 1 } }}>
        <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5, alignItems: out ? 'flex-end' : 'flex-start', minWidth: 0 }}>
          {item.texts.map((t, i) => (
            <Box
              key={i}
              sx={{
                px: 1.5, py: 1, borderRadius: '12px', maxWidth: 520, lineHeight: 1.5,
                ...(out ? { bgcolor: 'portide.accent', color: 'portide.accentText' } : { bgcolor: 'portide.bg', color: 'portide.fg', border: 1, borderColor: 'portide.line' }),
              }}
            >
              <TextBody text={t} out={out} />
            </Box>
          ))}
          {item.fileCount === 1 && <FileCard item={item} />}
          {item.fileCount > 1 && <FolderCard item={item} />}
          {item.texts.length === 0 && item.fileCount === 0 && item.summary && (
            // 没有收到任何文件的接收（停止或失败）：用报价摘要说明收的是什么。
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.25, p: 1.25, width: 300, maxWidth: '100%', borderRadius: '12px', border: 1, borderColor: 'portide.line', bgcolor: 'portide.bg', opacity: 0.8 }}>
              <FileTypeIcon kind={fileKind(item.summary.replace(/（.*$/, ''))} size={40} />
              <Typography noWrap title={item.summary} sx={{ flex: 1, minWidth: 0 }}>
                {item.summary}
              </Typography>
            </Box>
          )}
        </Box>
        <Tooltip title={t('更多')}>
          <IconButton className="msg-more" size="small" aria-label={t('更多操作')} onClick={openMenu} sx={{ opacity: 0, mt: 0.25, '&:focus-visible': { opacity: 1 } }}>
            <Ellipsis size={15} />
          </IconButton>
        </Tooltip>
      </Box>
      {state && (
        <Box
          data-state={state.state}
          sx={{
            display: 'flex', alignItems: 'center', gap: 0.5, mt: 0.5, fontSize: 12,
            color: state.state === 'failed' || state.state === 'partial' ? 'portide.err' : 'portide.muted',
          }}
        >
          {Icon && <Icon size={13} />}
          <span>{state.text}</span>
          {out && (state.state === 'failed' || state.state === 'partial' || state.state === 'cancelled') && (
            <Button size="small" startIcon={<RotateCw size={13} />} onClick={() => void run(() => resend(item), t('已重新加入发送队列'))} sx={{ height: 24, ml: 0.5 }}>
              {t('重发')}
            </Button>
          )}
        </Box>
      )}
      <Menu open={menu !== null} onClose={close()} anchorReference="anchorPosition" anchorPosition={menu ? { top: menu.y, left: menu.x } : undefined}>
        {item.texts.length > 0 && (
          <MenuItem onClick={close(copy)}>
            <ListItemIcon><Copy size={15} /></ListItemIcon>{t('复制')}
          </MenuItem>
        )}
        {item.fileCount === 1 && firstFile?.exists && (
          <MenuItem onClick={close(() => void run(() => Service.OpenFile(item.id, 0)))}>
            <ListItemIcon><ExternalLink size={15} /></ListItemIcon>{t('打开')}
          </MenuItem>
        )}
        {item.fileCount > 0 && firstFile?.exists && (
          <MenuItem onClick={close(() => void run(() => Service.RevealFile(item.id, 0)))}>
            <ListItemIcon><FolderOpen size={15} /></ListItemIcon>{t('在文件夹中显示')}
          </MenuItem>
        )}
        <MenuItem onClick={close(() => void run(() => Service.DeleteHistory(item.id), t('已删除这条记录')))} sx={{ color: 'portide.err' }}>
          <ListItemIcon><Trash2 size={15} /></ListItemIcon>{t('删除这条记录')}
        </MenuItem>
      </Menu>
    </Row>
  );
}

function Row({
  out, highlighted, testId, children, onContextMenu,
}: {
  out: boolean;
  highlighted: boolean;
  testId: string;
  children: ReactNode;
  onContextMenu?: (e: MouseEvent) => void;
}) {
  return (
    <Box
      data-testid={`item-${testId}`}
      data-highlighted={highlighted || undefined}
      onContextMenu={onContextMenu}
      sx={{
        display: 'flex', flexDirection: 'column', alignItems: out ? 'flex-end' : 'flex-start', px: 1, mx: -1, borderRadius: '12px',
        transition: 'background-color 0.6s', bgcolor: highlighted ? 'portide.accentSoft' : 'transparent',
      }}
    >
      {children}
    </Box>
  );
}
