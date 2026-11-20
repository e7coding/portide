// 设置的分类列表（M3c 设计 4.4）：通用、接收、通知与启动、网络与中继、存储与历史、外观、关于。
import { Box, Typography } from '@mui/material';
import { Bell, Download, HardDrive, Info, Network, Palette, SlidersHorizontal, type LucideIcon } from 'lucide-react';
import { ListPane, ListRow } from '../../components/shell/ListPane';
import { useNav, type SettingsSection } from '../../store/nav';
import { t } from '../../i18n';

export const SECTIONS: { key: SettingsSection; label: string; icon: LucideIcon }[] = [
  { key: 'general', get label() { return t('通用'); }, icon: SlidersHorizontal },
  { key: 'receive', get label() { return t('接收'); }, icon: Download },
  { key: 'notify', get label() { return t('通知与启动'); }, icon: Bell },
  { key: 'network', get label() { return t('网络与中继'); }, icon: Network },
  { key: 'storage', get label() { return t('存储与历史'); }, icon: HardDrive },
  { key: 'appearance', get label() { return t('外观'); }, icon: Palette },
  { key: 'about', get label() { return t('关于'); }, icon: Info },
];

export function SettingsNav() {
  const nav = useNav();
  return (
    <ListPane title={t('设置')}>
      {SECTIONS.map((s) => (
        <ListRow key={s.key} selected={nav.section === s.key} onClick={() => nav.openSection(s.key)} label={s.label}>
          <Box sx={{ display: 'grid', placeItems: 'center', color: nav.section === s.key ? 'portide.accent' : 'portide.muted' }}>
            <s.icon size={17} />
          </Box>
          <Typography sx={{ fontSize: 13.5, fontWeight: nav.section === s.key ? 600 : 400 }}>{s.label}</Typography>
        </ListRow>
      ))}
    </ListPane>
  );
}
