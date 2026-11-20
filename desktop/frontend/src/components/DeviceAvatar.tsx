// 设备头像（M3c 设计 3）：圆角方块里是设备类型图标，颜色按类型；右下角的小圆点表示在线（绿）或离线（灰）。
import { Box } from '@mui/material';
import { alpha } from '@mui/material/styles';
import { Layers } from 'lucide-react';
import { deviceIcon } from '../icons';
import { t } from '../i18n';

export interface DeviceAvatarProps {
  icon: string; // Device.icon；"all" 表示"全部设备"
  online?: boolean; // 不传时不显示圆点
  size?: number;
  ring?: string; // 圆点外圈的颜色，和头像所在的背景一致
}

export function DeviceAvatar({ icon, online, size = 34, ring = 'portide.list' }: DeviceAvatarProps) {
  const spec = icon === 'all' ? { icon: Layers, color: '#4F46E5', label: t('全部设备') } : deviceIcon(icon);
  const Icon = spec.icon;
  const dot = Math.max(9, Math.round(size * 0.3));
  return (
    <Box
      aria-hidden
      sx={{
        width: size, height: size, borderRadius: `${Math.round(size * 0.3)}px`, flexShrink: 0, position: 'relative',
        display: 'grid', placeItems: 'center', bgcolor: alpha(spec.color, 0.13), color: spec.color,
      }}
    >
      <Icon size={Math.round(size * 0.5)} strokeWidth={2} />
      {online !== undefined && (
        <Box
          data-online={online}
          sx={{
            position: 'absolute', right: -2, bottom: -2, width: dot, height: dot, borderRadius: '50%', border: 2, borderColor: ring,
            bgcolor: online ? 'portide.ok' : 'portide.muted',
          }}
        />
      )}
    </Box>
  );
}
