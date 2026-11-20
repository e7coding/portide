// 设备列表（M3c 设计 4.3）：本机一组、设备圈一组；每台显示头像（类型图标 + 在线点）、名字、状态
// （在线 · 局域网 / 离线 · 2 小时前）；底部"添加设备"。
import type { ReactNode } from 'react';
import { Box, Button, Typography } from '@mui/material';
import { Plus } from 'lucide-react';
import type { Device } from '../../api/portide';
import { useDevices } from '../../api/queries';
import { DeviceAvatar } from '../../components/DeviceAvatar';
import { ListPane, ListRow } from '../../components/shell/ListPane';
import { formatAgo } from '../../format';
import { PATH_ICONS } from '../../icons';
import { useNav } from '../../store/nav';
import { t } from '../../i18n';

/** 合并设备圈后还没跟过来的设备的状态文字（取代"离线"）。 */
export const MOVE_LABELS: Record<string, string> = {
  get waiting() { return t('等待合并'); },
  get outdated() { return t('需要升级'); },
  get left() { return t('没有跟过来'); },
  get elsewhere() { return t('去了别的设备圈'); },
};

/** 列表里的状态：本机 / 在线 · 局域网 / 离线 · 2 小时前。 */
export function shortStatus(d: Device): string {
  if (d.self) return t('本机');
  if (MOVE_LABELS[d.move]) return MOVE_LABELS[d.move];
  if (d.online) return [t('在线'), PATH_ICONS[d.path]?.label].filter(Boolean).join(' · ');
  return d.lastSeen ? t('离线 · {ago}', { ago: formatAgo(d.lastSeen) }) : t('离线 · 还没有连上过');
}

function Group({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Box sx={{ mb: 1 }}>
      <Typography variant="overline" component="h3" sx={{ display: 'block', px: 2, pt: 1, pb: 0.5, color: 'portide.muted' }}>
        {title}
      </Typography>
      {children}
    </Box>
  );
}

export function DeviceList() {
  const nav = useNav();
  const { data: devices = [] } = useDevices();
  const me = devices.find((d) => d.self);
  const others = devices.filter((d) => !d.self);
  const selected = nav.device || me?.keyId || '';
  const row = (d: Device) => (
    <ListRow key={d.keyId} selected={selected === d.keyId} onClick={() => nav.openDevice(d.keyId)} label={d.name}>
      <DeviceAvatar icon={d.icon} online={d.self ? undefined : d.online} />
      <Box sx={{ flex: 1, minWidth: 0 }}>
        <Typography noWrap sx={{ fontWeight: 600, fontSize: 13.5 }}>
          {d.name}
        </Typography>
        <Typography noWrap variant="caption" component="div" sx={{ color: d.online && !d.self ? 'portide.ok' : 'portide.muted' }}>
          {shortStatus(d)}
        </Typography>
      </Box>
    </ListRow>
  );
  return (
    <ListPane title={t('设备')}>
      <Box sx={{ display: 'flex', flexDirection: 'column', height: '100%' }}>
        <Box sx={{ flex: 1 }}>
          {me && <Group title={t('本机')}>{row(me)}</Group>}
          <Group title={t('设备圈 · {n} 台', { n: others.length })}>
            {others.length === 0 ? (
              <Typography variant="body2" sx={{ px: 2, py: 1, color: 'portide.muted' }}>
                {t('还没有其他设备')}
              </Typography>
            ) : (
              others.map(row)
            )}
          </Group>
        </Box>
        <Box sx={{ px: 1.5, pt: 1 }}>
          <Button fullWidth variant="tonal" startIcon={<Plus size={16} />} onClick={() => nav.setPairingOpen(true)}>
            {t('添加设备')}
          </Button>
        </Box>
      </Box>
    </ListPane>
  );
}
