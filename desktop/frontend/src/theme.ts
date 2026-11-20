// MUI 主题（Material UI v9）：M3c 的"清透"视觉（M3c 设计 3）——白底、浅灰图标栏和列表栏、靛蓝 #4F46E5 强调、
// 细边框、轻阴影；按钮 9px、卡片 12px、对话框 16px 圆角。颜色令牌同时写进 docs/界面规范.md，iOS 按它对齐。
import { createTheme, alpha, type Theme } from '@mui/material/styles';
import { enUS, zhCN } from '@mui/material/locale';

/** 界面颜色令牌。组件里用 sx 的调色板路径引用，例如 bgcolor: 'portide.rail'、color: 'portide.muted'。 */
export interface PortideTokens {
  bg: string; // 窗口底色、气泡（收到）、卡片
  rail: string; // 图标栏
  list: string; // 列表栏
  feed: string; // 内容区（消息流）底色
  fg: string; // 正文
  fg2: string; // 次一级正文
  muted: string; // 次要文字、图标
  line: string; // 分割线、边框
  chip: string; // 浅灰块：搜索框、工具按钮、日期分隔
  accent: string; // 强调色（选中、链接、发出的气泡）
  accentText: string; // 强调色上的文字
  accentSoft: string; // 强调浅底（拖放高亮、选中的分段按钮）
  grad: string; // 主按钮、应用图标的渐变
  ok: string;
  okSoft: string;
  warn: string;
  warnSoft: string;
  err: string;
  errSoft: string;
  selected: string; // 列表里选中项的底色（配 shadowSelected）
  shadowSelected: string;
  shadowPopover: string;
}

declare module '@mui/material/styles' {
  interface Palette {
    portide: PortideTokens;
  }
  interface PaletteOptions {
    portide?: PortideTokens;
  }
}
declare module '@mui/material/Button' {
  interface ButtonPropsVariantOverrides {
    tonal: true;
  }
}

export const TOKENS: Record<'light' | 'dark', PortideTokens> = {
  light: {
    bg: '#FFFFFF', rail: '#F3F4F8', list: '#F8F9FC', feed: '#FBFCFE',
    fg: '#111827', fg2: '#374151', muted: '#6B7280', line: '#E8EAF0', chip: '#EEF0F5',
    accent: '#4F46E5', accentText: '#FFFFFF', accentSoft: '#EEF0FF', grad: 'linear-gradient(135deg, #6366F1, #4F46E5)',
    ok: '#16A34A', okSoft: '#DCFCE7', warn: '#D97706', warnSoft: '#FEF3C7', err: '#DC2626', errSoft: '#FEE2E2',
    selected: '#FFFFFF', shadowSelected: '0 1px 3px rgb(17 24 39 / 0.08)', shadowPopover: '0 10px 30px rgb(17 24 39 / 0.12)',
  },
  dark: {
    bg: '#161922', rail: '#0F1117', list: '#12151C', feed: '#0D0F14',
    fg: '#F3F4F6', fg2: '#D1D5DB', muted: '#9CA3AF', line: '#262A35', chip: '#1E222C',
    accent: '#6366F1', accentText: '#FFFFFF', accentSoft: '#1E1B4B', grad: 'linear-gradient(135deg, #6366F1, #4F46E5)',
    ok: '#22C55E', okSoft: '#052E16', warn: '#F59E0B', warnSoft: '#3A2A05', err: '#F87171', errSoft: '#3B0D0D',
    selected: '#1E222C', shadowSelected: '0 1px 3px rgb(0 0 0 / 0.4)', shadowPopover: '0 10px 30px rgb(0 0 0 / 0.5)',
  },
};

/** 圆角（像素）：按钮和输入框、卡片和气泡、对话框。 */
export const RADIUS = { button: 9, card: 12, dialog: 16 };

// 使用系统字体，不打包字体文件：Linux 上是 Noto Sans CJK，Windows 上是微软雅黑。
export const fontSans = ['-apple-system', 'BlinkMacSystemFont', '"Segoe UI"', '"PingFang SC"', '"Noto Sans CJK SC"', '"Noto Sans SC"', '"Microsoft YaHei"', 'sans-serif'].join(',');
export const fontMono = ['ui-monospace', '"SF Mono"', '"JetBrains Mono"', '"Cascadia Mono"', 'Consolas', 'monospace'].join(',');

export function buildTheme(mode: 'light' | 'dark') {
  const t = TOKENS[mode];
  const focusRing = `0 0 0 3px ${alpha(t.accent, mode === 'light' ? 0.2 : 0.4)}`;
  return createTheme({
    palette: {
      mode,
      primary: { main: t.accent, contrastText: t.accentText },
      success: { main: t.ok }, warning: { main: t.warn }, error: { main: t.err },
      background: { default: t.bg, paper: t.bg },
      text: { primary: t.fg, secondary: t.muted },
      divider: t.line,
      action: { hover: alpha(t.fg, 0.05), selected: t.accentSoft, focus: alpha(t.fg, 0.1) },
      portide: t,
    },
    shape: { borderRadius: RADIUS.button },
    shadows: ['none', t.shadowSelected, t.shadowPopover, ...Array(22).fill(t.shadowPopover)] as any,
    typography: {
      fontFamily: fontSans,
      // 字号阶梯：xl 22/30、lg 18/26、md 16/24、base 14/22、sm 13/20、xs 12/18
      h1: { fontSize: 28, lineHeight: '36px', fontWeight: 700 },
      h2: { fontSize: 22, lineHeight: '30px', fontWeight: 700 },
      h3: { fontSize: 18, lineHeight: '26px', fontWeight: 600 },
      h4: { fontSize: 16, lineHeight: '24px', fontWeight: 600 },
      h5: { fontSize: 15, lineHeight: '22px', fontWeight: 600 },
      h6: { fontSize: 14, lineHeight: '22px', fontWeight: 600 },
      subtitle1: { fontSize: 14, lineHeight: '22px', fontWeight: 600 },
      subtitle2: { fontSize: 13, lineHeight: '20px', fontWeight: 600 },
      body1: { fontSize: 14, lineHeight: '22px' },
      body2: { fontSize: 13, lineHeight: '20px' },
      caption: { fontSize: 12, lineHeight: '18px' },
      overline: { fontSize: 11, lineHeight: '16px', fontWeight: 600, letterSpacing: 0.4, textTransform: 'none' },
      button: { fontSize: 14, lineHeight: '20px', fontWeight: 500, textTransform: 'none' },
    },
    components: {
      MuiCssBaseline: {
        styleOverrides: {
          body: { fontVariantNumeric: 'tabular-nums', WebkitFontSmoothing: 'antialiased', backgroundColor: t.bg },
          '@media (prefers-reduced-motion: reduce)': { '*': { animationDuration: '1ms !important', transitionDuration: '1ms !important' } },
          '::selection': { backgroundColor: alpha(t.accent, 0.25) },
        },
      },
      MuiButtonBase: { defaultProps: { disableRipple: true } },
      MuiButton: {
        defaultProps: { disableElevation: true },
        styleOverrides: {
          root: { borderRadius: RADIUS.button, height: 36, paddingInline: 16, whiteSpace: 'nowrap', '&.Mui-focusVisible': { boxShadow: focusRing } },
          sizeSmall: { height: 30, paddingInline: 12, fontSize: 13 },
          sizeLarge: { height: 42, paddingInline: 22, fontSize: 15 },
          outlined: { borderColor: t.line, color: t.fg, backgroundColor: t.bg, '&:hover': { backgroundColor: t.chip, borderColor: t.line } },
          text: { paddingInline: 10 },
        },
        variants: [
          // 主按钮：靛蓝渐变
          {
            props: { variant: 'contained', color: 'primary' },
            style: { background: t.grad, color: t.accentText, '&:hover': { background: t.grad, filter: 'brightness(1.06)' }, '&.Mui-disabled': { background: t.chip, color: t.muted } },
          },
          { props: { variant: 'contained', color: 'error' }, style: { backgroundColor: t.err, color: '#FFFFFF' } },
          { props: { variant: 'outlined', color: 'error' }, style: { borderColor: alpha(t.err, 0.4), color: t.err, '&:hover': { backgroundColor: t.errSoft, borderColor: t.err } } },
          // 次要按钮：浅灰底、正文色文字
          { props: { variant: 'tonal' }, style: { backgroundColor: t.chip, color: t.fg, '&:hover': { backgroundColor: alpha(t.fg, 0.1) } } },
        ],
      },
      MuiIconButton: {
        styleOverrides: {
          root: { color: t.muted, borderRadius: RADIUS.button, '&:hover': { backgroundColor: t.chip, color: t.fg }, '&.Mui-focusVisible': { boxShadow: focusRing } },
          sizeSmall: { padding: 5 },
        },
      },
      MuiToggleButtonGroup: {
        styleOverrides: { root: { borderRadius: RADIUS.button, backgroundColor: t.chip, padding: 3, gap: 2 }, grouped: { border: 0, borderRadius: '7px !important' } },
      },
      MuiToggleButton: {
        styleOverrides: {
          root: {
            height: 30, paddingInline: 12, border: 0, color: t.muted, textTransform: 'none', fontWeight: 500, fontSize: 13, gap: 6,
            '&.Mui-selected': { backgroundColor: t.selected, color: t.accent, boxShadow: t.shadowSelected, '&:hover': { backgroundColor: t.selected } },
          },
        },
      },
      MuiChip: {
        styleOverrides: {
          root: { height: 24, borderRadius: 7, fontWeight: 500, fontSize: 12 },
          sizeSmall: { height: 20, borderRadius: 6, fontSize: 11 },
          outlined: { borderColor: t.line },
          filled: ({ ownerState }) => (ownerState.color === 'default' || !ownerState.color ? { backgroundColor: t.chip, color: t.fg2 } : {}),
        },
      },
      MuiCard: {
        defaultProps: { variant: 'outlined' },
        styleOverrides: { root: { borderRadius: RADIUS.card, borderColor: t.line, backgroundImage: 'none', backgroundColor: t.bg } },
      },
      MuiPaper: { styleOverrides: { root: { backgroundImage: 'none' }, rounded: { borderRadius: RADIUS.card } } },
      MuiDialog: { styleOverrides: { paper: { borderRadius: RADIUS.dialog, backgroundColor: t.bg, boxShadow: t.shadowPopover } } },
      MuiDialogTitle: { styleOverrides: { root: { fontSize: 18, lineHeight: '26px', fontWeight: 600 } } },
      MuiMenu: { styleOverrides: { paper: { borderRadius: 10, border: `1px solid ${t.line}`, backgroundColor: t.bg, boxShadow: t.shadowPopover } } },
      MuiMenuItem: { styleOverrides: { root: { minHeight: 34, fontSize: 13, borderRadius: 6, marginInline: 4, gap: 10 } } },
      MuiPopover: { styleOverrides: { paper: { borderRadius: 10, border: `1px solid ${t.line}`, backgroundColor: t.bg, boxShadow: t.shadowPopover } } },
      MuiListItemIcon: { styleOverrides: { root: { minWidth: 28, color: 'inherit' } } },
      MuiOutlinedInput: {
        styleOverrides: {
          root: {
            borderRadius: 10, backgroundColor: t.bg,
            '&:hover:not(.Mui-focused):not(.Mui-error):not(.Mui-disabled) .MuiOutlinedInput-notchedOutline': { borderColor: alpha(t.fg, 0.25) },
          },
          notchedOutline: { borderColor: t.line },
        },
      },
      MuiTextField: { defaultProps: { size: 'small' } },
      MuiSelect: { defaultProps: { size: 'small' } },
      MuiSwitch: {
        styleOverrides: {
          switchBase: { '&.Mui-checked': { color: '#FFFFFF', '& + .MuiSwitch-track': { backgroundColor: t.accent, opacity: 1 } } },
          track: { backgroundColor: alpha(t.fg, 0.25), opacity: 1 },
        },
      },
      MuiAlert: { styleOverrides: { root: { borderRadius: 10 } } },
      MuiLinearProgress: { styleOverrides: { root: { height: 4, borderRadius: 2, backgroundColor: t.chip }, bar: { borderRadius: 2 } } },
      MuiTooltip: { styleOverrides: { tooltip: { backgroundColor: mode === 'light' ? '#111827' : '#F3F4F6', color: mode === 'light' ? '#FFFFFF' : '#111827', fontSize: 12, borderRadius: 7 } } },
      MuiDivider: { styleOverrides: { root: { borderColor: t.line } } },
    },
  });
}

/** MUI 自带的文字（"没有选项"等）跟随界面语言。 */
export function localized(theme: Theme, lang: 'zh' | 'en' = 'zh') {
  return createTheme(theme, lang === 'en' ? enUS : zhCN);
}
