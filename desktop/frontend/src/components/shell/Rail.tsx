// 图标栏（M3c 设计 2）：应用图标；时间线、传输（角标是正在传输的数量）、设备；最下面是设置。
import { Badge, Box, Tooltip } from '@mui/material';
import { ArrowLeftRight, MessagesSquare, MonitorSmartphone, Settings, type LucideIcon } from 'lucide-react';
import { AppMark } from '../AppMark';
import type { Page } from '../../store/nav';
import { t } from '../../i18n';

export interface RailItem {
  page: Page;
  label: string;
  icon: LucideIcon;
  shortcut: string;
}

export const RAIL_ITEMS: RailItem[] = [
  { page: 'timeline', get label() { return t('时间线'); }, icon: MessagesSquare, shortcut: 'Ctrl+1' },
  { page: 'transfers', get label() { return t('传输'); }, icon: ArrowLeftRight, shortcut: 'Ctrl+2' },
  { page: 'devices', get label() { return t('设备'); }, icon: MonitorSmartphone, shortcut: 'Ctrl+3' },
  { page: 'settings', get label() { return t('设置'); }, icon: Settings, shortcut: 'Ctrl+4' },
];

export function Rail({ page, onChange, active, update = false }: { page: Page; onChange: (p: Page) => void; active: number; update?: boolean }) {
  const button = (it: RailItem) => {
    const on = it.page === page;
    return (
      <Tooltip key={it.page} title={t('{label}（{shortcut}）', { label: it.label, shortcut: it.shortcut })} placement="right">
        <Box
          component="button"
          type="button"
          aria-label={it.label}
          aria-current={on ? 'page' : undefined}
          onClick={() => onChange(it.page)}
          sx={{
            width: 38, height: 38, borderRadius: '10px', border: 0, cursor: 'pointer', display: 'grid', placeItems: 'center',
            bgcolor: on ? 'portide.selected' : 'transparent', color: on ? 'portide.accent' : 'portide.muted', boxShadow: on ? 1 : 'none',
            '&:hover': { color: on ? 'portide.accent' : 'portide.fg', bgcolor: on ? 'portide.selected' : 'portide.chip' },
            '&:focus-visible': { outline: 2, outlineColor: 'portide.accent', outlineOffset: 1 },
          }}
        >
          {it.page === 'transfers' ? (
            <Badge
              badgeContent={active}
              color="primary"
              max={99}
              slotProps={{ badge: { 'aria-label': t('{n} 个正在传输', { n: active }), sx: { fontSize: 10, height: 16, minWidth: 16, px: 0.5 } } as object }}
            >
              <it.icon size={19} />
            </Badge>
          ) : it.page === 'settings' && update ? (
            <Badge variant="dot" color="primary" slotProps={{ badge: { 'aria-label': t('有新版本') } as object }}>
              <it.icon size={19} />
            </Badge>
          ) : (
            <it.icon size={19} />
          )}
        </Box>
      </Tooltip>
    );
  };
  return (
    <Box
      component="nav"
      aria-label={t('主导航')}
      sx={{
        width: 56, flexShrink: 0, bgcolor: 'portide.rail', borderRight: 1, borderColor: 'portide.line',
        display: 'flex', flexDirection: 'column', alignItems: 'center', gap: 1, py: 1.5,
      }}
    >
      <Box aria-hidden sx={{ mb: 1 }}>
        <AppMark size={32} />
      </Box>
      {RAIL_ITEMS.slice(0, 3).map(button)}
      <Box sx={{ flex: 1 }} />
      {button(RAIL_ITEMS[3])}
    </Box>
  );
}
