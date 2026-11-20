// 设置的内容区（M3c 设计 4.4）：显示列表栏里选中的分类。每项改动立即生效并提示"已保存"，不用再点"保存"。
import { Box, Typography } from '@mui/material';
import type { State } from '../../api/portide';
import { useNav } from '../../store/nav';
import { About, Appearance } from './Appearance';
import { General, NotifyStartup } from './General';
import { Network } from './Network';
import { Receive } from './Receive';
import { SECTIONS } from './SettingsNav';
import { Storage } from './Storage';

export function SettingsPage({ state }: { state: State }) {
  const nav = useNav();
  const title = SECTIONS.find((s) => s.key === nav.section)?.label ?? '';
  return (
    <Box sx={{ height: '100%', overflowY: 'auto', bgcolor: 'portide.feed' }}>
      <Box sx={{ maxWidth: 680, mx: 'auto', px: 3, py: 3 }}>
        <Typography variant="h2" component="h1" sx={{ mb: 2 }}>
          {title}
        </Typography>
        {nav.section === 'general' && <General state={state} />}
        {nav.section === 'receive' && <Receive />}
        {nav.section === 'notify' && <NotifyStartup />}
        {nav.section === 'network' && <Network />}
        {nav.section === 'storage' && <Storage state={state} />}
        {nav.section === 'appearance' && <Appearance />}
        {nav.section === 'about' && <About state={state} />}
      </Box>
    </Box>
  );
}
