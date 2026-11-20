// 设备详情（M3c 设计 4.3）：头部是大头像、名字 + 铅笔（改名）、在线和连接方式、"发消息"；信息卡：连接方式、地址、
// 最近在线、加入时间、设备指纹（分组显示，可复制）；设置卡：设备图标（四选一，只存在本机）；最下面是红色的"移出设备圈"，
// 二次确认（暂时确认不了设备圈是否已同步时再确认一次）。本机详情：改名、设备指纹、版本信息。
import { useState, type ReactNode } from 'react';
import {
  Alert, Box, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, IconButton, TextField,
  ToggleButton, ToggleButtonGroup, Tooltip, Typography,
} from '@mui/material';
import { useQueryClient } from '@tanstack/react-query';
import { Check, Copy, MessageSquare, Pencil, Trash2, X } from 'lucide-react';
import { Clipboard } from '@wailsio/runtime';
import { Service, type Device } from '../../api/portide';
import { errorText } from '../../api/errors';
import { useDevices, useRenameCheck } from '../../api/queries';
import { useRun } from '../../api/useRun';
import { VERSION } from '../../about';
import { DeviceAvatar } from '../../components/DeviceAvatar';
import { formatAgo, formatMonthDay, formatTime, renameBlockedText } from '../../format';
import { DEVICE_ICONS, PATH_ICONS, type DeviceIconName } from '../../icons';
import { fontMono } from '../../theme';
import { useNav } from '../../store/nav';
import { MOVE_LABELS } from './DeviceList';
import { useToast } from '../../store/toast';
import { lang, t } from '../../i18n';

/** 设备指纹按 4 个字符一组显示。 */
export function groupFingerprint(keyId: string): string {
  return keyId.match(/.{1,4}/g)?.join(' ') ?? keyId;
}

/** 迁移状态的说明（合并设备圈设计 5.3）。 */
function moveNote(d: Device): string {
  switch (d.move) {
    case 'waiting': {
      const until = new Date(d.moveUntil);
      const date = lang() === 'en' ? formatMonthDay(until.getTime()) : `${until.getMonth() + 1} 月 ${until.getDate()} 日`; // i18n-ignore
      return t('它和这里任意一台合并过来的设备连上后会自动加入；30 天后不再等待（{date}）', { date });
    }
    case 'outdated':
      return t('它的 Portide 版本太旧，升级之后和这里任意一台合并过来的设备连上时会自动加入');
    case 'left':
      return t('它没有跟过来（已经等了 30 天）。可以把它移出设备圈，或者在它上面再输入一次这个设备圈的配对码');
    case 'elsewhere':
      return t('原来设备圈里的部分设备合并到了别的设备圈，把那个设备圈和这里再合并一次即可');
    default:
      return '';
  }
}

export function DeviceDetail() {
  const nav = useNav();
  const { data: devices = [] } = useDevices();
  const d = devices.find((x) => x.keyId === nav.device) ?? devices.find((x) => x.self);
  if (!d) return null;
  return <Detail key={d.keyId} d={d} />;
}

function Detail({ d }: { d: Device }) {
  const nav = useNav();
  const qc = useQueryClient();
  const run = useRun();
  const toast = useToast();
  const { data: check } = useRenameCheck();
  const [editing, setEditing] = useState(false);
  const [name, setName] = useState(d.name);
  const [removing, setRemoving] = useState(false);
  const [needsForce, setNeedsForce] = useState(false);
  const [failure, setFailure] = useState('');
  const [busy, setBusy] = useState(false);
  const path = PATH_ICONS[d.path];
  const canRename = check?.allowed ?? false;
  const renameBlocked = check && !check.allowed ? renameBlockedText(check.missing) : '';

  const saveName = async () => {
    const n = name.trim();
    if (n === d.name) {
      setEditing(false);
      return;
    }
    try {
      await Service.RenameDevice(d.keyId, n);
      toast.success(t('已改名，其他设备连上后会同步新名字'));
      setEditing(false);
      void qc.invalidateQueries({ queryKey: ['devices'] });
      void qc.invalidateQueries({ queryKey: ['state'] });
    } catch (err) {
      toast.error(errorText(err));
    }
  };
  const closeRemove = () => {
    setRemoving(false);
    setNeedsForce(false);
    setFailure('');
  };
  const remove = async () => {
    setFailure('');
    setBusy(true);
    try {
      const res = await Service.RemoveDevice(d.keyId, needsForce);
      if (res.needsForce) {
        setNeedsForce(true);
        return;
      }
      toast.success(
        t('已把「{name}」移出设备圈', { name: d.name }) + (res.relayWarning ? t('。已配置中继：请在中继服务器上换一个新令牌，然后在任意一台设备上更新中继，会自动同步到其余设备') : ''),
      );
      closeRemove();
      nav.openDevice('');
      void qc.invalidateQueries({ queryKey: ['devices'] });
    } catch (err) {
      setFailure(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Box sx={{ height: '100%', overflowY: 'auto', bgcolor: 'portide.feed' }}>
      <Box sx={{ maxWidth: 640, mx: 'auto', px: 3, py: 3 }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 2 }}>
          <DeviceAvatar icon={d.icon} online={d.self ? undefined : d.online} size={60} ring="portide.feed" />
          <Box sx={{ flex: 1, minWidth: 0 }}>
            {editing ? (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                <TextField
                  autoFocus
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  onKeyDown={(e) => {
                    // 输入法组字确认的 Enter 不保存；空名字也不保存（和保存按钮的禁用条件一致）。
                    if (e.key === 'Enter' && !e.nativeEvent.isComposing && e.nativeEvent.keyCode !== 229 && name.trim() !== '') void saveName();
                    if (e.key === 'Escape') {
                      e.stopPropagation();
                      setEditing(false);
                    }
                  }}
                  slotProps={{ htmlInput: { 'aria-label': t('设备名'), maxLength: 64 } }}
                  sx={{ maxWidth: 280 }}
                />
                <IconButton aria-label={t('保存名字')} onClick={() => void saveName()} disabled={name.trim() === ''}>
                  <Check size={17} />
                </IconButton>
                <IconButton aria-label={t('取消改名')} onClick={() => setEditing(false)}>
                  <X size={17} />
                </IconButton>
              </Box>
            ) : (
              <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                <Typography variant="h2" component="h1" noWrap>
                  {d.name}
                </Typography>
                <Tooltip title={renameBlocked || t('改名')}>
                  <span>
                    <IconButton
                      aria-label={t('改名')}
                      disabled={!canRename}
                      onClick={() => {
                        setName(d.name);
                        setEditing(true);
                      }}
                    >
                      <Pencil size={16} />
                    </IconButton>
                  </span>
                </Tooltip>
              </Box>
            )}
            <Box sx={{ display: 'flex', gap: 0.75, mt: 0.5, flexWrap: 'wrap' }}>
              {d.self ? (
                <Chip>{t('本机')}</Chip>
              ) : (
                <>
                  <Chip color={d.online ? 'portide.ok' : undefined}>{MOVE_LABELS[d.move] ?? (d.online ? t('在线') : t('离线'))}</Chip>
                  {d.online && path && (
                    <Chip>
                      <path.icon size={12} />
                      {path.label}
                    </Chip>
                  )}
                </>
              )}
            </Box>
          </Box>
          {!d.self && (
            <Button variant="contained" startIcon={<MessageSquare size={16} />} onClick={() => nav.openConversation(d.keyId)}>
              {t('发消息')}
            </Button>
          )}
        </Box>
        {!d.self && moveNote(d) && (
          <Alert severity={d.move === 'waiting' ? 'info' : 'warning'} sx={{ mt: 1.5 }}>
            {moveNote(d)}
          </Alert>
        )}
        {renameBlocked && (
          <Typography variant="caption" sx={{ display: 'block', mt: 1, color: 'portide.muted' }}>
            {renameBlocked}
          </Typography>
        )}

        <Card title={t('信息')}>
          {!d.self && <Info label={t('连接方式')}>{d.online ? (path?.label ?? '-') : (MOVE_LABELS[d.move] ?? t('离线'))}</Info>}
          {!d.self && <Info label={t('地址')}>{d.path === 'relay' ? (d.addr ? t('经中继（{addr}）', { addr: d.addr }) : t('经中继')) : d.addr ? <Mono>{d.addr}</Mono> : '-'}</Info>}
          {!d.self && <Info label={t('最近在线')}>{d.online ? t('现在') : formatAgo(d.lastSeen)}</Info>}
          <Info label={t('加入时间')}>{d.joinedAt ? formatTime(d.joinedAt) : '-'}</Info>
          <Info label={t('设备指纹')}>
            <Box sx={{ display: 'flex', alignItems: 'flex-start', gap: 0.5 }}>
              <Mono>{groupFingerprint(d.keyId)}</Mono>
              <Tooltip title={t('复制设备指纹')}>
                <IconButton size="small" aria-label={t('复制设备指纹')} onClick={() => void run(() => Clipboard.SetText(d.keyId), t('设备指纹已复制'))}>
                  <Copy size={14} />
                </IconButton>
              </Tooltip>
            </Box>
          </Info>
          {d.self && <Info label={t('版本')}>Portide {VERSION}</Info>}
        </Card>

        <Card title={t('设备图标')}>
          <Typography variant="body2" sx={{ color: 'portide.muted', mb: 1.25 }}>
            {t('只影响本机上的显示，不会同步给其他设备。')}
          </Typography>
          <ToggleButtonGroup
            exclusive
            value={d.icon}
            aria-label={t('设备图标')}
            onChange={(_, v: DeviceIconName | null) => v && void run(() => Service.SetDeviceIcon(d.keyId, v))}
          >
            {(Object.keys(DEVICE_ICONS) as DeviceIconName[]).map((k) => {
              const spec = DEVICE_ICONS[k];
              return (
                <ToggleButton key={k} value={k} aria-label={spec.label}>
                  <spec.icon size={15} />
                  {spec.label}
                </ToggleButton>
              );
            })}
          </ToggleButtonGroup>
        </Card>

        {!d.self && (
          <Box sx={{ mt: 4, pt: 2.5, borderTop: 1, borderColor: 'portide.line' }}>
            <Button color="error" variant="outlined" startIcon={<Trash2 size={16} />} onClick={() => setRemoving(true)}>
              {t('移出设备圈')}
            </Button>
            <Typography variant="caption" sx={{ display: 'block', mt: 0.75, color: 'portide.muted' }}>
              {t('例如手机丢了：移除之后它无法再连接设备圈里的任何设备。')}
            </Typography>
          </Box>
        )}
      </Box>

      <Dialog open={removing} onClose={closeRemove}>
        <DialogTitle>{t('移出「{name}」？', { name: d.name })}</DialogTitle>
        <DialogContent>
          {failure && (
            <Alert severity="error" sx={{ mb: 2 }}>
              {failure}
            </Alert>
          )}
          {needsForce ? (
            <Alert severity="warning">
              {t('暂时确认不了设备圈是否已同步到最新（没有其他设备在线）。如果已经在别的设备上移除过它，请不要重复移除——重复移除会让设备圈分裂。')}
            </Alert>
          ) : (
            <DialogContentText>{t('移除之后它将无法再连接设备圈里的设备。这个操作不能撤销。')}</DialogContentText>
          )}
        </DialogContent>
        <DialogActions>
          <Button variant="tonal" onClick={closeRemove}>
            {t('取消')}
          </Button>
          <Button variant="contained" color="error" disabled={busy} onClick={() => void remove()}>
            {needsForce ? t('仍要移除') : t('移除')}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}

function Chip({ children, color }: { children: ReactNode; color?: string }) {
  return (
    <Box component="span" sx={{ display: 'inline-flex', alignItems: 'center', gap: 0.5, px: 1, height: 22, borderRadius: '7px', fontSize: 12, bgcolor: 'portide.chip', color: color ?? 'portide.fg2' }}>
      {children}
    </Box>
  );
}

function Card({ title, children }: { title: string; children: ReactNode }) {
  return (
    <Box component="section" aria-label={title} sx={{ mt: 2.5, p: 2, borderRadius: '12px', border: 1, borderColor: 'portide.line', bgcolor: 'portide.bg' }}>
      <Typography variant="subtitle1" component="h2" sx={{ mb: 1.25 }}>
        {title}
      </Typography>
      {children}
    </Box>
  );
}

function Info({ label, children }: { label: string; children: ReactNode }) {
  return (
    <Box sx={{ display: 'flex', gap: 2, py: 0.75, borderTop: 1, borderColor: 'portide.line', '&:first-of-type': { borderTop: 0 } }}>
      <Typography variant="body2" sx={{ width: 84, flexShrink: 0, color: 'portide.muted' }}>
        {label}
      </Typography>
      <Box sx={{ flex: 1, minWidth: 0, fontSize: 13 }}>{children}</Box>
    </Box>
  );
}

function Mono({ children }: { children: ReactNode }) {
  return (
    <Box component="span" sx={{ fontFamily: fontMono, fontSize: 12.5, wordBreak: 'break-all' }}>
      {children}
    </Box>
  );
}
