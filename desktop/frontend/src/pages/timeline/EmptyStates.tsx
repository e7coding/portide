// 会话里的空状态（M3c 设计 4.1）：新会话是一个大拖放区和说明；还没有其他设备时引导去添加设备。
import { Box, Button, Typography } from '@mui/material';
import { MonitorSmartphone, Upload } from 'lucide-react';
import { useQuery } from '@tanstack/react-query';
import { Service, type Device } from '../../api/portide';
import { PATH_ICONS } from '../../icons';
import { t } from '../../i18n';

export function EmptyDrop({ device }: { device: Device }) {
  const how = !device.online
    ? t('{name}现在离线，发出的内容会在它上线后送达。', { name: device.name })
    : device.path === 'relay'
      ? t('{name}在线，经中继传输。', { name: device.name })
      : t('{name}在线，通过{path}直接传输。', { name: device.name, path: PATH_ICONS[device.path]?.label ?? t('局域网') });
  return (
    <Box sx={{ flex: 1, display: 'grid', placeItems: 'center' }}>
      <Box sx={{ width: 340, maxWidth: '100%', border: 2, borderStyle: 'dashed', borderColor: 'portide.accent', borderRadius: '16px', bgcolor: 'portide.accentSoft', textAlign: 'center', px: 3, py: 3.5, color: 'portide.accent' }}>
        <Upload size={30} />
        <Typography sx={{ mt: 1, color: 'portide.fg' }}>{t('把文件或文件夹拖到这里')}</Typography>
        <Typography sx={{ fontWeight: 600, color: 'portide.fg' }}>{t('发给「{name}」', { name: device.name })}</Typography>
        <Typography variant="body2" sx={{ mt: 1, color: 'portide.muted' }}>
          {t('也可以在下面输入文字，或点回形针选择文件。')}
          <br />
          {how}
        </Typography>
      </Box>
    </Box>
  );
}

export function NoDevices({ onAdd }: { onAdd: (tab: 'show' | 'enter') => void }) {
  // 附近有设备正在显示配对码时提示一下（每 5 秒查一次 mDNS）。
  const { data: nearby } = useQuery({ queryKey: ['nearby'], queryFn: Service.NearbyInviters, refetchInterval: 5000 });
  return (
    <Box sx={{ flex: 1, display: 'grid', placeItems: 'center' }}>
      <Box sx={{ width: 360, maxWidth: '100%', textAlign: 'center' }}>
        <Box sx={{ width: 56, height: 56, borderRadius: '16px', mx: 'auto', display: 'grid', placeItems: 'center', bgcolor: 'portide.accentSoft', color: 'portide.accent' }}>
          <MonitorSmartphone size={28} />
        </Box>
        <Typography variant="h3" sx={{ mt: 2 }}>
          {t('设备圈里还没有其他设备')}
        </Typography>
        <Typography variant="body2" sx={{ mt: 1, color: 'portide.muted' }}>
          {t('在另一台设备上打开 Portide：一台显示配对码，另一台输入它。')}
        </Typography>
        {nearby && (
          <Typography variant="body2" sx={{ mt: 1, color: 'portide.accent' }}>
            {t('附近有设备正在等待配对')}
          </Typography>
        )}
        <Box sx={{ display: 'flex', gap: 1.5, justifyContent: 'center', mt: 2.5 }}>
          <Button variant="contained" onClick={() => onAdd('show')}>
            {t('显示配对码')}
          </Button>
          <Button variant="tonal" onClick={() => onAdd('enter')}>
            {t('输入配对码')}
          </Button>
        </Box>
      </Box>
    </Box>
  );
}

export function NoHistory() {
  return (
    <Box sx={{ flex: 1, display: 'grid', placeItems: 'center' }}>
      <Typography sx={{ color: 'portide.muted' }}>{t('还没有收发记录。选择设备、输入文字或拖入文件就可以发送。')}</Typography>
    </Box>
  );
}
