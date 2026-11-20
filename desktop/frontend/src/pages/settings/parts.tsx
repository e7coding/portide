// 设置页的公共部件（M3c 设计 4.4）：分组卡片、一行设置（标题、说明、右侧控件）、改动即保存并提示"已保存"、
// 带"自定义"输入框的分段按钮。
import { useEffect, useState, type ReactNode } from 'react';
import { Box, TextField, ToggleButton, ToggleButtonGroup, Typography } from '@mui/material';
import { useQueryClient } from '@tanstack/react-query';
import { errorText } from '../../api/errors';
import { useToast } from '../../store/toast';
import { lang, t } from '../../i18n';

/** 一组设置。分类里只有这一组时（标题和页面标题相同）不再重复显示标题，只用作无障碍名称。 */
export function SettingsCard({ title, children, showTitle = true }: { title: string; children: ReactNode; showTitle?: boolean }) {
  return (
    <Box component="section" aria-label={title} sx={{ mb: 2.5, borderRadius: '12px', border: 1, borderColor: 'portide.line', bgcolor: 'portide.bg', px: 2, py: 0.5 }}>
      {showTitle && (
        <Typography variant="subtitle1" component="h2" sx={{ pt: 1.5, pb: 0.5 }}>
          {title}
        </Typography>
      )}
      {children}
    </Box>
  );
}

/** 一行设置：左边标题和说明，右边控件；below 是放在下面一整行的内容（例如自定义输入框、地址列表）。 */
export function SettingRow({ title, hint, control, below, dim = false }: { title: ReactNode; hint?: ReactNode; control?: ReactNode; below?: ReactNode; dim?: boolean }) {
  return (
    <Box sx={{ py: 1.5, borderTop: 1, borderColor: 'portide.line', '&:first-of-type': { borderTop: 0 }, opacity: dim ? 0.55 : 1 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
        <Box sx={{ flex: 1, minWidth: 0 }}>
          <Typography sx={{ fontWeight: 500 }}>{title}</Typography>
          {hint && (
            <Typography variant="body2" component="div" sx={{ color: 'portide.muted' }}>
              {hint}
            </Typography>
          )}
        </Box>
        {control && <Box sx={{ flexShrink: 0 }}>{control}</Box>}
      </Box>
      {below && <Box sx={{ mt: 1.25 }}>{below}</Box>}
    </Box>
  );
}

/** 改动立即生效：执行操作，成功时提示"已保存"（或给定的文字），失败时用红色提示原因；之后刷新相关查询。返回是否成功。 */
export function useSave() {
  const qc = useQueryClient();
  const toast = useToast();
  return async (action: () => Promise<unknown>, keys: string[] = ['settings'], done = t('已保存')) => {
    let ok = true;
    try {
      await action();
      if (done) toast.success(done);
    } catch (err) {
      toast.error(errorText(err));
      ok = false;
    }
    for (const k of keys) void qc.invalidateQueries({ queryKey: [k] });
    return ok;
  };
}

export interface Choice {
  value: number;
  label: string;
}

/**
 * 分段按钮加"自定义"：value 不在选项里时显示为自定义。选"自定义"后在下面输入数字（单位 unit），失焦或回车时保存；
 * 不是 min 到 max 之间的整数时提示并恢复。scale 是输入框里的数字和实际值之间的倍数（例如 MB）。
 */
export function ChoiceField({
  label, value, choices, onSave, unit, min, max, scale = 1, disabled = false,
}: {
  label: string;
  value: number;
  choices: Choice[];
  onSave: (v: number) => Promise<boolean>;
  unit: string;
  min: number;
  max: number;
  scale?: number;
  disabled?: boolean;
}) {
  const toast = useToast();
  const known = choices.some((c) => c.value === value);
  const [custom, setCustom] = useState(!known);
  const [text, setText] = useState(String(Math.round(value / scale)));
  // 只在保存下来的值变了时同步（选项数组每次渲染都是新的，不能放进依赖）。
  useEffect(() => {
    setCustom(!known);
    setText(String(Math.round(value / scale)));
  }, [value, scale, known]);
  const commit = async () => {
    const n = Number(text);
    if (text.trim() === '' || !Number.isInteger(n) || n < min || n > max) {
      toast.error(t('请输入 {min} 到 {max} 之间的整数', { min, max }));
      setText(String(Math.round(value / scale)));
      return;
    }
    if (n * scale !== value) await onSave(n * scale);
  };
  return (
    <Box>
      <ToggleButtonGroup
        exclusive
        disabled={disabled}
        aria-label={label}
        value={custom ? 'custom' : value}
        onChange={(_, v: number | 'custom' | null) => {
          if (v === null) return;
          if (v === 'custom') {
            setCustom(true);
            return;
          }
          setCustom(false);
          void onSave(v);
        }}
      >
        {choices.map((c) => (
          <ToggleButton key={c.value} value={c.value}>
            {c.label}
          </ToggleButton>
        ))}
        <ToggleButton value="custom">{t('自定义')}</ToggleButton>
      </ToggleButtonGroup>
      {custom && !disabled && (
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, mt: 1 }}>
          <TextField
            type="number"
            value={text}
            onChange={(e) => setText(e.target.value)}
            onBlur={() => void commit()}
            onKeyDown={(e) => e.key === 'Enter' && void commit()}
            slotProps={{ htmlInput: { 'aria-label': `${label}（${unit}）`, min, max } }}
            sx={{ width: 120 }}
          />
          <Typography variant="body2" sx={{ color: 'portide.muted' }}>
            {unit}
          </Typography>
        </Box>
      )}
    </Box>
  );
}

/** 保存位置的友好写法："下载 › Portide"（只取最后两级；中文界面里常见的英文目录名换成中文）。 */
export function friendlyPath(path: string): string {
  // 系统目录本来就是英文名，英文界面原样显示。
  const names: Record<string, string> =
    lang() === 'en' ? {} : { Downloads: '下载', Documents: '文档', Desktop: '桌面', Pictures: '图片', Videos: '视频', Music: '音乐' }; // i18n-ignore
  const segs = path.split(/[\\/]+/).filter(Boolean);
  return segs.slice(-2).map((s) => names[s] ?? s).join(' › ') || path;
}
