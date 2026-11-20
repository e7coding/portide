// 输入配对码并加入（合并设备圈设计 5.1）：8 位数字，"找不到对方？"里可以填地址。
// 添加设备对话框的"输入配对码"页用它；结果（加入或反向加入）交给 onDone，错误留在表单里。
import { useState, type FormEvent } from 'react';
import { Alert, Box, Button, ButtonBase, CircularProgress, Collapse, InputBase, TextField, Typography } from '@mui/material';
import { ChevronDown, ChevronRight } from 'lucide-react';
import { Service, type JoinResult } from '../api/portide';
import { errorText } from '../api/errors';
import { fontMono } from '../theme';
import { t } from '../i18n';

/** 配对码输入：只留数字，满 4 位后自动加空格，显示成"1234 5678"。 */
export function formatCodeInput(text: string): string {
  const digits = text.replace(/\D/g, '').slice(0, 8);
  return digits.length > 4 ? `${digits.slice(0, 4)} ${digits.slice(4)}` : digits;
}

/** others 是本机设备圈里其他设备的名字；不为空时先说明三种结果里"合并"会发生什么。 */
export function JoinCodeForm({ others, onDone }: { others: string[]; onDone: (r: JoinResult) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [code, setCode] = useState('');
  const [addr, setAddr] = useState('');
  const [more, setMore] = useState(false);
  const digits = code.replace(/\D/g, '');
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (digits.length !== 8 || busy) return;
    setBusy(true);
    setError('');
    try {
      onDone(await Service.Join(digits, addr.trim()));
    } catch (err) {
      setError(errorText(err));
      setBusy(false);
    }
  };
  const who = others.length > 1 ? t('「{name}」等 {n} 台', { name: others[0], n: others.length }) : t('「{name}」', { name: others[0] });
  return (
    <Box component="form" onSubmit={submit} sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
      <Typography sx={{ color: 'portide.muted' }}>{t('在另一台设备上打开"添加设备"，输入它显示的 8 位数字。')}</Typography>
      {others.length > 0 && (
        <Typography variant="body2" sx={{ color: 'portide.muted' }}>
          {t('如果对方只有它自己，它会加入这个设备圈；如果对方也有其他设备，两个设备圈会合并：这台设备所在的设备圈（还有{who}）并入对方的。历史记录和设置都保留。', { who })}
        </Typography>
      )}
      {error && <Alert severity="error">{error}</Alert>}
      <InputBase
        autoFocus
        value={code}
        disabled={busy}
        onChange={(e) => setCode(formatCodeInput(e.target.value))}
        placeholder="1234 5678"
        inputProps={{ 'aria-label': t('配对码'), inputMode: 'numeric' }}
        sx={{
          fontFamily: fontMono, fontSize: 32, fontWeight: 700, letterSpacing: 6, borderRadius: '12px', border: 2,
          borderColor: digits.length === 8 ? 'portide.accent' : 'portide.line', bgcolor: 'portide.list', px: 2, py: 1, '& input': { textAlign: 'center' },
        }}
      />
      <Box>
        <ButtonBase onClick={() => setMore(!more)} aria-expanded={more} sx={{ gap: 0.5, color: 'portide.muted', fontSize: 13, borderRadius: '6px' }}>
          {more ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
          {t('找不到对方？')}
        </ButtonBase>
        <Collapse in={more}>
          <TextField
            fullWidth
            label={t('对方地址（可不填）')}
            placeholder="192.168.1.5:7443"
            helperText={t('不填时在局域网里自动寻找；不在同一个网段时，填对方设置 › 网络里显示的本机地址')}
            value={addr}
            onChange={(e) => setAddr(e.target.value)}
            sx={{ mt: 1.5 }}
          />
        </Collapse>
      </Box>
      {busy && (
        <Typography sx={{ color: 'portide.muted', display: 'flex', alignItems: 'center', gap: 1 }}>
          <CircularProgress size={15} />
          {t('正在寻找并连接…')}
        </Typography>
      )}
      <Box sx={{ display: 'flex', justifyContent: 'flex-end' }}>
        <Button type="submit" variant="contained" disabled={busy || digits.length !== 8}>
          {t('加入')}
        </Button>
      </Box>
    </Box>
  );
}
