// 会话（M3c 设计 4.1）：头部（头像、名字、在线状态和连接方式、地址；"收到的文件"和"更多"里的筛选），
// 待确认的接收、消息流、发送区。"全部设备"是所有设备的混合时间线，每条标出来自 / 发给哪台设备。
import { useState } from 'react';
import { Box, Button, IconButton, ListItemIcon, Menu, MenuItem, Tooltip, Typography } from '@mui/material';
import { Check, Ellipsis, FolderOpen } from 'lucide-react';
import { Service, type Device, type TimelineItem } from '../../api/portide';
import { useDevices, useTimeline } from '../../api/queries';
import { useRun } from '../../api/useRun';
import { DeviceAvatar } from '../../components/DeviceAvatar';
import { formatAgo, isTextSummary } from '../../format';
import { PATH_ICONS } from '../../icons';
import { ALL_DROP } from '../../store/drop';
import { useNav } from '../../store/nav';
import { Composer } from './Composer';
import { EmptyDrop, NoDevices, NoHistory } from './EmptyStates';
import { Feed } from './Feed';
import { InboxCards } from './InboxCards';
import { t } from '../../i18n';

type Filter = 'all' | 'files' | 'texts';
const FILTERS: { key: Filter; label: string }[] = [
  { key: 'all', get label() { return t('全部消息'); } },
  { key: 'files', get label() { return t('只看文件'); } },
  { key: 'texts', get label() { return t('只看文字'); } },
];

function applyFilter(items: TimelineItem[], f: Filter): TimelineItem[] {
  if (f === 'files') return items.filter((i) => i.fileCount > 0 || (i.kind === 'task' && !isTextSummary(i.summary)));
  if (f === 'texts') return items.filter((i) => i.texts.length > 0 || (i.kind === 'task' && isTextSummary(i.summary)));
  return items;
}

/** 头部的第二行：在线 · 局域网 · 192.168.1.5 / 离线 · 2 小时前在线。 */
export function deviceStatusLine(d: Device): string {
  if (!d.online) return d.lastSeen ? t('离线 · {ago}在线', { ago: formatAgo(d.lastSeen) }) : t('离线 · 还没有连上过');
  const parts = [t('在线'), PATH_ICONS[d.path]?.label ?? ''];
  if (d.addr && d.path !== 'relay') parts.push(d.addr);
  return parts.filter(Boolean).join(' · ');
}

export function ConversationView() {
  const nav = useNav();
  const run = useRun();
  const { data: devices, isSuccess } = useDevices();
  const others = (devices ?? []).filter((d) => !d.self);
  // 会话对应的设备已被移除：回到"全部设备"。
  const device = others.find((d) => d.keyId === nav.conversation) ?? null;
  const key = device ? device.keyId : '';
  const { data: items = [] } = useTimeline(key);
  const [filter, setFilter] = useState<Filter>('all');
  const [more, setMore] = useState<HTMLElement | null>(null);

  if (isSuccess && others.length === 0) {
    return <NoDevices onAdd={(tab) => nav.setPairingOpen(true, tab)} />;
  }
  const online = others.filter((d) => d.online).length;
  const shown = applyFilter(items, filter);
  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%', minHeight: 0 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.25, px: 2.5, py: 1.25, borderBottom: 1, borderColor: 'portide.line', minHeight: 60 }}>
        <DeviceAvatar icon={device ? device.icon : 'all'} size={36} />
        <Box sx={{ flex: 1, minWidth: 0 }}>
          <Typography variant="h4" component="h1" noWrap>
            {device ? device.name : t('全部设备')}
          </Typography>
          <Typography variant="caption" sx={{ color: device?.online ? 'portide.ok' : 'portide.muted' }} noWrap component="div">
            {device ? deviceStatusLine(device) : t('{n} 台设备 · {online} 台在线', { n: others.length, online })}
          </Typography>
        </Box>
        <Button size="small" variant="tonal" startIcon={<FolderOpen size={15} />} onClick={() => void run(() => Service.OpenSaveDir())}>
          {t('收到的文件')}
        </Button>
        <Tooltip title={t('筛选')}>
          <IconButton size="small" aria-label={t('筛选消息')} onClick={(e) => setMore(e.currentTarget)} sx={{ bgcolor: filter === 'all' ? 'portide.chip' : 'portide.accentSoft', color: filter === 'all' ? undefined : 'portide.accent' }}>
            <Ellipsis size={16} />
          </IconButton>
        </Tooltip>
        <Menu open={more !== null} anchorEl={more} onClose={() => setMore(null)}>
          {FILTERS.map((f) => (
            <MenuItem
              key={f.key}
              selected={filter === f.key}
              onClick={() => {
                setFilter(f.key);
                setMore(null);
              }}
            >
              <ListItemIcon>{filter === f.key && <Check size={15} />}</ListItemIcon>
              {f.label}
            </MenuItem>
          ))}
        </Menu>
      </Box>
      <InboxCards peerName={device ? device.name : null} />
      <Feed
        items={shown}
        all={!device}
        dropPeer={device ? device.keyId : ALL_DROP}
        dropText={device ? (device.online ? t('松开发给「{name}」', { name: device.name }) : t('松开发给「{name}」，它上线后自动送达', { name: device.name })) : t('松开发给发送区选中的设备')}
        highlight={nav.highlight}
        onHighlightDone={nav.clearHighlight}
        empty={filter !== 'all' ? <NoHistory /> : device ? <EmptyDrop device={device} /> : <NoHistory />}
      />
      <Composer conversation={key} devices={devices ?? []} />
    </Box>
  );
}
