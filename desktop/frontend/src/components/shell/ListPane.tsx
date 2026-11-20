// 列表栏（M3c 设计 2）：约 232px 宽，内容随页面变化（会话、设备、设置分类）；顶部是标题和一个可选的操作按钮。
import type { ReactNode } from 'react';
import { Box, Typography } from '@mui/material';

export function ListPane({ title, action, children, label }: { title: string; action?: ReactNode; children: ReactNode; label?: string }) {
  return (
    <Box
      component="aside"
      aria-label={label ?? title}
      sx={{ width: 232, flexShrink: 0, bgcolor: 'portide.list', borderRight: 1, borderColor: 'portide.line', display: 'flex', flexDirection: 'column', minHeight: 0 }}
    >
      <Box sx={{ display: 'flex', alignItems: 'center', px: 1.75, pt: 1.75, pb: 1, minHeight: 52 }}>
        <Typography variant="h4" component="h2" sx={{ flex: 1 }}>
          {title}
        </Typography>
        {action}
      </Box>
      <Box sx={{ flex: 1, minHeight: 0, overflowY: 'auto', pb: 1 }}>{children}</Box>
    </Box>
  );
}

/** 列表栏里的一行（会话、设备、设置分类共用的外观）：选中时白底加极淡的阴影。 */
export function ListRow({
  selected, onClick, children, label, dropPeer, dropLabel,
}: {
  selected: boolean;
  onClick: () => void;
  children: ReactNode;
  label: string;
  dropPeer?: string; // 设置后这一行是拖放目标：文件落在它上面时发给这台设备（见 store/drop.ts）
  dropLabel?: string; // 拖到这一行上方时显示的提示，例如"松开发给「手机」"
}) {
  const drop = dropPeer !== undefined ? { 'data-file-drop-target': true, 'data-peer': dropPeer, 'data-drop-label': dropLabel } : {};
  return (
    <Box
      component="button"
      type="button"
      aria-label={label}
      aria-current={selected ? 'true' : undefined}
      onClick={onClick}
      {...drop}
      sx={{
        all: 'unset', boxSizing: 'border-box', display: 'flex', alignItems: 'center', gap: 1.25, width: 'calc(100% - 12px)', mx: '6px', px: 1.25, py: 1,
        borderRadius: '10px', cursor: 'pointer', position: 'relative', color: 'portide.fg',
        bgcolor: selected ? 'portide.selected' : 'transparent', boxShadow: selected ? 1 : 'none',
        '&:hover': { bgcolor: selected ? 'portide.selected' : 'portide.chip' },
        '&:focus-visible': { outline: 2, outlineColor: 'portide.accent', outlineOffset: -2 },
        // 拖放经过时（Wails 运行时加上 file-drop-target-active 类）：虚线高亮，提示盖在这一行上
        // （列表栏会滚动，提示放到行外面会被裁掉）。
        '&.file-drop-target-active': { outline: 2, outlineStyle: 'dashed', outlineColor: 'portide.accent', outlineOffset: -2 },
        '&.file-drop-target-active::after': {
          content: 'attr(data-drop-label)', position: 'absolute', inset: 0, borderRadius: '10px', px: 1.5, pointerEvents: 'none',
          display: 'flex', alignItems: 'center', justifyContent: 'center', textAlign: 'center', lineHeight: 1.35,
          bgcolor: 'portide.accentSoft', color: 'portide.accent', fontSize: 12, fontWeight: 600,
        },
      }}
    >
      {children}
    </Box>
  );
}
