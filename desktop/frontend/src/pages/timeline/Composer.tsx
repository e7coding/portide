// 发送区（M3c 设计 4.1）：附件按钮（文件、文件夹）、输入框、发送按钮。在某台设备的会话里目标固定是它；
// 在"全部设备"里先选设备（可多选，记住上次的选择）。Enter 发送、Shift+Enter 换行；Ctrl+V 可以粘贴图片或文件。
import { useState, type ClipboardEvent, type KeyboardEvent } from 'react';
import { Box, Button, Checkbox, IconButton, InputBase, ListItemText, Menu, MenuItem, Tooltip, Typography } from '@mui/material';
import { ChevronDown, FolderUp, Paperclip, SendHorizontal } from 'lucide-react';
import { useQueryClient } from '@tanstack/react-query';
import { Platform, Service, type Device, type SendResult } from '../../api/portide';
import { errorText } from '../../api/errors';
import { useToast } from '../../store/toast';
import { chooseTargets, loadTargets, saveTargets } from '../../store/targets';
import { quoteNames } from '../../format';
import { t } from '../../i18n';

/** 粘贴的内容上限（和服务层一致）。 */
export const PASTE_MAX_BYTES = 64 << 20;

const pad = (n: number) => String(n).padStart(2, '0');

/** 粘贴的截图没有像样的名字（浏览器给的是 image.png）：换成"截图 2026-10-26 14.03.05.png"。 */
export function pastedName(file: File, now = new Date()): string {
  if (file.name && file.name !== 'image.png') return file.name;
  const ext = file.type === 'image/jpeg' ? 'jpg' : file.type.startsWith('image/') ? file.type.slice(6) : 'png';
  const d = now;
  const stamp = `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}.${pad(d.getMinutes())}.${pad(d.getSeconds())}`;
  return t('截图 {time}', { time: stamp }) + `.${ext}`;
}

function readDataURL(file: File): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(new Error(t('无法读取粘贴的内容')));
    r.readAsDataURL(file);
  });
}

export function Composer({ conversation, devices }: { conversation: string; devices: Device[] }) {
  const qc = useQueryClient();
  const toast = useToast();
  const others = devices.filter((d) => !d.self);
  const [stored, setStored] = useState<string[]>(loadTargets);
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [picker, setPicker] = useState<HTMLElement | null>(null);

  const fixed = conversation !== '';
  const targets = fixed ? (others.some((d) => d.keyId === conversation) ? [conversation] : []) : chooseTargets(devices, stored);
  const names = targets.map((k) => others.find((d) => d.keyId === k)?.name ?? k);

  const toggle = (key: string) => {
    const next = targets.includes(key) ? targets.filter((k) => k !== key) : [...targets, key];
    saveTargets(next);
    setStored(next);
  };

  const send = async (action: () => Promise<SendResult>) => {
    setBusy(true);
    try {
      const res = await action();
      if (res.skipped.length > 0) toast.warning(t('跳过了 {n} 项（符号链接、特殊文件或无法读取）：{list}', { n: res.skipped.length, list: res.skipped.join(t('、')) }));
      void qc.invalidateQueries({ queryKey: ['timeline'] });
      void qc.invalidateQueries({ queryKey: ['conversations'] });
      return true;
    } catch (err) {
      toast.error(errorText(err));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const sendText = async () => {
    if (text.trim() === '' || targets.length === 0 || busy) return;
    if (await send(() => Service.SendText(targets, text))) setText('');
  };
  // 对话框本身也可能失败（例如桌面运行时出错）：报告出来，不留下未处理的 Promise 拒绝。
  const pickFiles = async () => {
    try {
      const paths = await Platform.PickFiles();
      if (paths.length > 0) await send(() => Service.SendFiles(targets, paths));
    } catch (err) {
      toast.error(errorText(err));
    }
  };
  const pickFolder = async () => {
    try {
      const dir = await Platform.PickFolder(t('选择要发送的文件夹'));
      if (dir) await send(() => Service.SendFiles(targets, [dir]));
    } catch (err) {
      toast.error(errorText(err));
    }
  };
  const onKeyDown = (e: KeyboardEvent) => {
    // Enter 发送、Shift+Enter 换行；输入法正在组字时的 Enter 是确认候选词，不发送。
    // isComposing 在 WebKit 上不可靠：确认候选词的 Enter 有时 isComposing 为 false，但 keyCode 仍是 229。
    if (e.key === 'Enter' && !e.shiftKey && !e.nativeEvent.isComposing && e.nativeEvent.keyCode !== 229) {
      e.preventDefault();
      void sendText();
    }
  };
  const onPaste = (e: ClipboardEvent) => {
    const files = Array.from(e.clipboardData?.files ?? []);
    if (files.length === 0) return; // 纯文字：照常粘贴进输入框
    e.preventDefault();
    if (targets.length === 0) {
      toast.warning(t('先选择要发给哪台设备'));
      return;
    }
    void (async () => {
      for (const f of files) {
        if (f.size > PASTE_MAX_BYTES) {
          toast.error(t('「{name}」太大，请先保存成文件再发送', { name: f.name || t('粘贴的内容') }));
          continue;
        }
        let data: string;
        try {
          data = await readDataURL(f);
        } catch (err) {
          toast.error(errorText(err));
          continue;
        }
        await send(() => Service.SendPasted(targets, pastedName(f), data));
      }
    })();
  };

  if (others.length === 0) {
    return (
      <Box sx={{ borderTop: 1, borderColor: 'portide.line', px: 2, py: 1.5 }}>
        <Typography variant="body2" sx={{ color: 'portide.muted' }}>
          {t('设备圈里还没有其他设备，添加之后就能在这里发送')}
        </Typography>
      </Box>
    );
  }
  const placeholder =
    targets.length === 0 ? t('先选择要发给哪台设备') : t('发给{names}… 也可以拖入文件或粘贴图片', { names: quoteNames(names) });
  const disabled = busy || targets.length === 0;
  return (
    <Box sx={{ borderTop: 1, borderColor: 'portide.line', px: 2, py: 1.25, bgcolor: 'portide.bg' }}>
      {!fixed && (
        <Box sx={{ mb: 1 }}>
          <Button size="small" variant="tonal" endIcon={<ChevronDown size={14} />} onClick={(e) => setPicker(e.currentTarget)} aria-haspopup="menu">
            {targets.length === 0 ? t('选择设备') : t('发给：{names}', { names: names.join(t('、')) })}
          </Button>
          <Menu open={picker !== null} anchorEl={picker} onClose={() => setPicker(null)}>
            {others.map((d) => (
              <MenuItem key={d.keyId} onClick={() => toggle(d.keyId)} dense>
                <Checkbox size="small" checked={targets.includes(d.keyId)} sx={{ p: 0, mr: 1 }} slotProps={{ input: { 'aria-label': d.name } }} />
                <ListItemText primary={d.name} secondary={d.online ? t('在线') : t('离线，上线后送达')} />
              </MenuItem>
            ))}
          </Menu>
        </Box>
      )}
      <Box sx={{ display: 'flex', alignItems: 'flex-end', gap: 1 }}>
        <Tooltip title={t('发送文件')}>
          <span>
            <IconButton aria-label={t('发送文件')} disabled={disabled} onClick={() => void pickFiles()} sx={{ bgcolor: 'portide.chip', width: 34, height: 34 }}>
              <Paperclip size={17} />
            </IconButton>
          </span>
        </Tooltip>
        <Tooltip title={t('发送文件夹')}>
          <span>
            <IconButton aria-label={t('发送文件夹')} disabled={disabled} onClick={() => void pickFolder()} sx={{ bgcolor: 'portide.chip', width: 34, height: 34 }}>
              <FolderUp size={17} />
            </IconButton>
          </span>
        </Tooltip>
        <InputBase
          multiline
          maxRows={6}
          fullWidth
          value={text}
          placeholder={placeholder}
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onKeyDown}
          onPaste={onPaste}
          inputProps={{ 'aria-label': t('输入要发送的文字') }}
          sx={{ flex: 1, px: 1.5, py: 0.75, minHeight: 36, borderRadius: '10px', border: 1, borderColor: 'portide.line', bgcolor: 'portide.list', fontSize: 14, '&.Mui-focused': { borderColor: 'portide.accent' } }}
        />
        <Tooltip title={t('发送（Enter）')}>
          <span>
            <IconButton
              aria-label={t('发送')}
              disabled={disabled || text.trim() === ''}
              onClick={() => void sendText()}
              sx={{ width: 36, height: 36, borderRadius: '10px', background: (t) => t.palette.portide.grad, color: '#fff', '&:hover': { color: '#fff', filter: 'brightness(1.06)' }, '&.Mui-disabled': { background: 'none', bgcolor: 'portide.chip' } }}
            >
              <SendHorizontal size={17} />
            </IconButton>
          </span>
        </Tooltip>
      </Box>
    </Box>
  );
}
