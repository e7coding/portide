// "添加设备"对话框（M3c 设计 4.3，合并设备圈设计 5.1）。顶部两页：显示配对码（默认）/ 输入配对码。
// 显示配对码有三种状态：
// 等待中：配对码大字号、分两组显示，可以复制，倒计时，新设备上的操作步骤，"正在等待新设备…"；
// 成功："「工作本」已加入设备圈"（对方的设备圈只有它自己时），或"已加入「电脑」的设备圈，现在可以和…互传了"（本机被对方带走时）；
// 失败或过期：原因 + "重新生成"。
// 输入配对码是 JoinCodeForm，成功后在对话框里显示结果（5.2）。
// 切到输入配对码、关闭对话框（包括按 Esc）都会取消还在等待的配对。
import { useEffect, useState } from 'react';
import { Box, Button, CircularProgress, Dialog, DialogActions, DialogContent, DialogTitle, IconButton, Tooltip, Typography } from '@mui/material';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import { CircleAlert, CircleCheck, Copy } from 'lucide-react';
import { Clipboard } from '@wailsio/runtime';
import { Service, type JoinResult, type Pairing } from '../api/portide';
import { errorText } from '../api/errors';
import { useDevices } from '../api/queries';
import { useRun } from '../api/useRun';
import { formatCountdown, quoteNames } from '../format';
import { fontMono } from '../theme';
import { useNav } from '../store/nav';
import { JoinCodeForm } from './JoinCodeForm';
import { t } from '../i18n';

const steps = () => [t('在新设备上安装并打开 Portide'), t('选"输入配对码"，输入上面的 8 位数字'), t('两台设备在同一个局域网里时会自动找到对方')];

export function PairingDialog() {
  const nav = useNav();
  const qc = useQueryClient();
  const run = useRun();
  const open = nav.pairingOpen;
  const [round, setRound] = useState(0); // "重新生成"时加一，重新开放配对
  const [started, setStarted] = useState<Pairing | null>(null);
  const [error, setError] = useState('');
  const [now, setNow] = useState(Date.now());
  const [tab, setTab] = useState<'show' | 'enter'>('show');
  const [joinResult, setJoinResult] = useState<JoinResult | null>(null);
  // 每次打开按 nav.pairingTab 决定先显示哪一页。在渲染中同步重置（而不是放进 effect），
  // 否则开始配对的 effect 会先按旧的页签跑一次，在"输入配对码"页白白开放又取消一个配对。
  const [wasOpen, setWasOpen] = useState(false);
  if (open !== wasOpen) {
    setWasOpen(open);
    if (open) {
      setTab(nav.pairingTab);
      setJoinResult(null);
    }
  }
  const { data: devices = [] } = useDevices();
  const otherNames = devices.filter((d) => !d.self).map((d) => d.name);

  useEffect(() => {
    if (!open) return;
    let cancelled = false;
    setStarted(null);
    setError('');
    if (tab !== 'show') return;
    Service.StartPairing().then(
      (p) => (cancelled ? void Service.CancelPairing(p.id) : setStarted(p)),
      (err) => !cancelled && setError(errorText(err)),
    );
    const tick = window.setInterval(() => setNow(Date.now()), 1000);
    return () => {
      cancelled = true;
      window.clearInterval(tick);
    };
  }, [open, round, tab]);


  const { data: status } = useQuery({
    queryKey: ['pairing', started?.id],
    queryFn: () => Service.PairingStatus(started!.id),
    enabled: open && tab === 'show' && started !== null,
  });
  const p = status?.id === started?.id ? (status ?? started) : started;
  const waiting = p?.status === 'waiting' && p.expiresAt > now;
  const failed = !!error || p?.status === 'failed' || p?.status === 'expired' || (p?.status === 'waiting' && !waiting);
  const joined = p?.status === 'done' ? devices.find((d) => !d.self && d.name === p.joinerName) : undefined;

  const close = () => {
    if (p && waiting) void Service.CancelPairing(p.id);
    void qc.invalidateQueries({ queryKey: ['devices'] });
    nav.setPairingOpen(false);
  };
  const regenerate = () => {
    if (p && waiting) void Service.CancelPairing(p.id);
    setRound((r) => r + 1);
  };
  const switchTab = (next: 'show' | 'enter') => {
    if (next === tab) return;
    if (p && waiting) void Service.CancelPairing(p.id);
    setTab(next);
  };
  const onJoined = (r: JoinResult) => {
    setJoinResult(r);
    void qc.invalidateQueries({ queryKey: ['devices'] });
  };
  // 本机被对方带走（adopt）后，设备圈里除本机外的设备名。
  const peers = quoteNames(otherNames);
  const nameCounts = new Map<string, number>();
  for (const d of devices) nameCounts.set(d.name, (nameCounts.get(d.name) ?? 0) + 1);
  const hasDuplicate = [...nameCounts.values()].some((c) => c > 1);
  // outdated 每台需要升级的设备一项（同名的会重复）：按出现次数和 waiting 对应，用完的重名项用"还没跟过来"。
  const waitingLines = (() => {
    const left = new Map<string, number>();
    for (const n of joinResult?.outdated ?? []) left.set(n, (left.get(n) ?? 0) + 1);
    return (joinResult?.waiting ?? []).map((n) => {
      const c = left.get(n) ?? 0;
      if (c > 0) {
        left.set(n, c - 1);
        return t('「{name}」的 Portide 版本太旧，需要先升级才能跟过来', { name: n });
      }
      return t('「{name}」还没跟过来，它和这里任意一台合并过来的设备连上时会自动跟过来（30 天内）', { name: n });
    });
  })();
  const groups = p ? p.code.replace(/\D/g, '').match(/.{1,4}/g) ?? [] : [];

  return (
    <Dialog open={open} onClose={close} maxWidth="xs" fullWidth aria-labelledby="pairing-title">
      <DialogTitle id="pairing-title">{t('添加设备')}</DialogTitle>
      <DialogContent>
        {!joinResult && p?.status !== 'done' && (
          <Box sx={{ display: 'flex', gap: 1, mb: 2 }}>
            <Button variant={tab === 'show' ? 'contained' : 'tonal'} size="small" aria-pressed={tab === 'show'} onClick={() => switchTab('show')}>
              {t('显示配对码')}
            </Button>
            <Button variant={tab === 'enter' ? 'contained' : 'tonal'} size="small" aria-pressed={tab === 'enter'} onClick={() => switchTab('enter')}>
              {t('输入配对码')}
            </Button>
          </Box>
        )}
        {tab === 'enter' && !joinResult && <JoinCodeForm others={otherNames} onDone={onJoined} />}
        {joinResult && (
          <Box sx={{ textAlign: 'center', py: 2 }}>
            <Box sx={{ color: 'portide.ok' }}>
              <CircleCheck size={40} />
            </Box>
            <Typography variant="h3" sx={{ mt: 1 }}>
              {joinResult.mode === 'merge'
                ? t('已合并，设备圈里现在有 {n} 台设备', { n: joinResult.devices })
                : joinResult.mode === 'adopt'
                  ? t('「{name}」已加入设备圈', { name: joinResult.peerName })
                  : t('已加入「{name}」的设备圈', { name: joinResult.peerName })}
            </Typography>
            <Typography variant="body2" sx={{ color: 'portide.muted', mt: 0.5 }}>
              {t('现在就可以互相发送文字和文件了')}
            </Typography>
            {joinResult.mode === 'merge' &&
              waitingLines.map((line, i) => (
                <Typography key={i} variant="body2" sx={{ color: 'portide.muted', mt: 0.5 }}>
                  {line}
                </Typography>
              ))}
            {joinResult.mode === 'merge' && hasDuplicate && (
              <Typography variant="body2" sx={{ color: 'portide.muted', mt: 0.5 }}>
                {t('有设备重名，可以在设备详情里改名')}
              </Typography>
            )}
          </Box>
        )}
        {tab === 'show' && !p && !error && (
          <Box sx={{ display: 'grid', placeItems: 'center', py: 4 }}>
            <CircularProgress size={26} />
          </Box>
        )}
        {tab === 'show' && p && waiting && (
          <Box sx={{ textAlign: 'center' }}>
            <Typography variant="body2" sx={{ color: 'portide.muted' }}>
              {t('在新设备上输入这个配对码')}
            </Typography>
            <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 1, my: 1.5 }}>
              <Box aria-label={t('配对码 {code}', { code: p.code })} sx={{ display: 'flex', gap: 1.5, fontFamily: fontMono, fontSize: 34, fontWeight: 700, letterSpacing: 3, color: 'portide.fg' }}>
                {groups.map((g, i) => (
                  <Box key={i} component="span" sx={{ px: 1.25, py: 0.25, borderRadius: '10px', bgcolor: 'portide.chip' }}>
                    {g}
                  </Box>
                ))}
              </Box>
              <Tooltip title={t('复制配对码')}>
                <IconButton aria-label={t('复制配对码')} onClick={() => void run(() => Clipboard.SetText(p.code.replace(/\D/g, '')), t('配对码已复制'))}>
                  <Copy size={17} />
                </IconButton>
              </Tooltip>
            </Box>
            <Typography variant="caption" sx={{ color: 'portide.muted' }}>
              {t('{time} 内有效', { time: formatCountdown(p.expiresAt - now) })}
            </Typography>
            <Box component="ol" sx={{ textAlign: 'left', bgcolor: 'portide.list', borderRadius: '12px', border: 1, borderColor: 'portide.line', py: 1.25, pr: 2, pl: 4.5, my: 2, '& li': { py: 0.25, fontSize: 13, color: 'portide.fg2' } }}>
              {steps().map((s) => (
                <li key={s}>{s}</li>
              ))}
            </Box>
            <Typography variant="body2" sx={{ color: 'portide.muted', display: 'flex', alignItems: 'center', justifyContent: 'center', gap: 1 }}>
              <CircularProgress size={13} />
              {t('正在等待新设备…')}
            </Typography>
          </Box>
        )}
        {tab === 'show' && p?.status === 'done' && (
          <Box sx={{ textAlign: 'center', py: 2 }}>
            <Box sx={{ color: 'portide.ok' }}>
              <CircleCheck size={40} />
            </Box>
            <Typography variant="h3" sx={{ mt: 1 }}>
              {p.mode === 'adopt'
                ? t('已加入「{name}」的设备圈，现在可以和{peers}互传了', { name: p.joinerName, peers })
                : p.mode === 'merge'
                  ? t('已合并：「{name}」和它设备圈里的 {n} 台设备加入了设备圈', { name: p.joinerName, n: p.brought })
                  : t('「{name}」已加入设备圈', { name: p.joinerName })}
            </Typography>
            {p.mode !== 'adopt' && (
              <Typography variant="body2" sx={{ color: 'portide.muted', mt: 0.5 }}>
                {t('现在就可以互相发送文字和文件了')}
              </Typography>
            )}
          </Box>
        )}
        {tab === 'show' && failed && p?.status !== 'done' && (
          <Box sx={{ textAlign: 'center', py: 2 }}>
            <Box sx={{ color: 'portide.err' }}>
              <CircleAlert size={40} />
            </Box>
            <Typography variant="h3" sx={{ mt: 1 }}>
              {error ? t('无法开始配对') : p?.status === 'failed' ? t('配对失败') : t('配对码已过期')}
            </Typography>
            <Typography variant="body2" sx={{ color: 'portide.muted', mt: 0.5 }}>
              {error || (p?.status === 'failed' ? p.error : t('配对码 5 分钟内有效，请重新生成一个'))}
            </Typography>
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        {joinResult ? (
          <Button variant="contained" onClick={close}>
            {t('完成')}
          </Button>
        ) : tab === 'show' && p?.status === 'done' ? (
          <>
            {joined && (
              <Button
                variant="tonal"
                onClick={() => {
                  close();
                  nav.openConversation(joined.keyId);
                }}
              >
                {t('给它发消息')}
              </Button>
            )}
            <Button variant="contained" onClick={close}>
              {t('完成')}
            </Button>
          </>
        ) : tab === 'show' && failed ? (
          <>
            <Button variant="tonal" onClick={close}>
              {t('关闭')}
            </Button>
            <Button variant="contained" onClick={regenerate}>
              {t('重新生成')}
            </Button>
          </>
        ) : (
          <Button variant="tonal" onClick={close}>
            {t('取消')}
          </Button>
        )}
      </DialogActions>
    </Dialog>
  );
}
