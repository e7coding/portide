// 文件类型图标（M3c 设计 3）：浅色底配同色图标，颜色按类型（见 icons.ts）。
import { Box } from '@mui/material';
import { alpha } from '@mui/material/styles';
import { FILE_KINDS, type FileKind } from '../icons';

export function FileTypeIcon({ kind, size = 36 }: { kind: FileKind; size?: number }) {
  const spec = FILE_KINDS[kind];
  return (
    <Box
      aria-hidden
      data-kind={kind}
      sx={{
        width: size, height: size, borderRadius: `${Math.round(size * 0.24)}px`, flexShrink: 0,
        display: 'grid', placeItems: 'center', bgcolor: alpha(spec.color, 0.13), color: spec.color,
      }}
    >
      <spec.icon size={Math.round(size * 0.5)} />
    </Box>
  );
}
