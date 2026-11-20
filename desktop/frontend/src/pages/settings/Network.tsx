// 设置 › 网络与中继（M3c 设计 4.4、5.3）：中继（状态、地址、指纹、添加 / 删除）、监听端口（默认 7443 / 自动选择 / 自定义，
// 重启后生效）、本机地址（自动发现不工作时，在对方那里手动填写）。
import { useState } from 'react';
import { Box, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle, IconButton, TextField, Tooltip, Typography } from '@mui/material';
import { Copy } from 'lucide-react';
import { Clipboard } from '@wailsio/runtime';
import { Service } from '../../api/portide';
import type { RelayEntry } from '../../api/portide';
import { useLocalAddresses, useRelay, useSettings } from '../../api/queries';
import { useRun } from '../../api/useRun';
import { useToast } from '../../store/toast';
import { fontMono } from '../../theme';
import { ChoiceField, SettingRow, SettingsCard, useSave } from './parts';
import { t } from '../../i18n';

const PORTS = [
  { value: 7443, get label() { return t('默认 7443'); } },
  { value: 0, get label() { return t('自动选择'); } },
];

const pad = (n: number) => String(n).padStart(2, '0');
const formatChanged = (ms: number) => {
  const t = new Date(ms);
  return `${pad(t.getMonth() + 1)}-${pad(t.getDate())} ${pad(t.getHours())}:${pad(t.getMinutes())}`;
};

const Mono = ({ children }: { children: string }) => (
  <Box component="span" sx={{ fontFamily: fontMono, fontSize: 12.5, wordBreak: 'break-all' }}>
    {children}
  </Box>
);

export function Network() {
  const save = useSave();
  const run = useRun();
  const toast = useToast();
  const { data: relay } = useRelay();
  const { data: settings } = useSettings();
  const { data: addrs = [] } = useLocalAddresses();
  const [config, setConfig] = useState('');
  const [removing, setRemoving] = useState<RelayEntry | null>(null);
  const relays = relay?.relays ?? [];
  const syncNote =
    t('中继设置会自动同步到设备圈里的所有设备') + (relay && relay.changedAt > 0 ? t(' · 最后由「{name}」在 {time} 修改', { name: relay.changedBy, time: formatChanged(relay.changedAt) }) : '');
  // 新增还是更新：按添加前后 id 是否已存在判断。
  const portNote = !settings
    ? ''
    : settings.listenPort === 0
      ? t('现在监听 {port}；自动选择的端口每次启动可能不同', { port: settings.actualPort })
      : settings.listenPort !== settings.actualPort
        ? t('现在监听 {port}；重启 Portide 后改用 {next}，手动填过本机地址的设备要跟着改', { port: settings.actualPort, next: settings.listenPort })
        : t('现在监听 {port}；改了之后重启 Portide 生效', { port: settings.actualPort });
  const add = async () => {
    const ids = async (fallback: string[]) => {
      try {
        return (await Service.Relay()).relays.map((r) => r.id);
      } catch {
        return fallback;
      }
    };
    const before = new Set(await ids(relays.map((r) => r.id)));
    const ok = await save(() => Service.AddRelay(config), ['relay'], '');
    if (!ok) return;
    // 查不到添加之后的列表时，不能把已经成功的添加当成失败：按"新增"提示。
    let fresh = true;
    try {
      fresh = (await Service.Relay()).relays.some((r) => !before.has(r.id));
    } catch {
      /* 保持 true */
    }
    toast.success(fresh ? t('已添加中继，几秒内就会连接') : t('已更新中继'));
    setConfig('');
  };
  return (
    <>
      <SettingsCard title={t('中继')}>
        {relays.length === 0 ? (
          <SettingRow title={t('状态')} hint={t('没有配置中继：只在局域网内连接')} />
        ) : (
          relays.map((r) => (
            <SettingRow
              key={r.id}
              title={<Mono>{r.addr}</Mono>}
              hint={r.status}
              control={
                <Button size="small" color="error" variant="outlined" aria-label={t('删除 {addr}', { addr: r.addr })} onClick={() => setRemoving(r)}>
                  {t('删除')}
                </Button>
              }
            />
          ))
        )}
        <Box sx={{ py: 1.5, borderTop: 1, borderColor: 'portide.line', display: 'flex', flexDirection: 'column', gap: 0.5 }}>
          <Typography variant="body2" sx={{ color: 'portide.muted' }}>
            {syncNote}
          </Typography>
          {(relay?.outdated ?? []).map((name) => (
            <Typography key={name} variant="body2" sx={{ color: 'warning.main' }}>
              {t('「{name}」的 Portide 版本较旧，收不到中继设置的更改，需要在它上面手动设置', { name })}
            </Typography>
          ))}
        </Box>
        <SettingRow
          title={t('添加中继')}
          hint={t('跨网络（不在同一个局域网）时用；配置串由中继服务器的 portide-relay token add 生成')}
          below={
            <Box sx={{ display: 'flex', gap: 1, alignItems: 'flex-start' }}>
              <TextField
                fullWidth
                multiline
                maxRows={3}
                placeholder="portide-relay://…"
                value={config}
                onChange={(e) => setConfig(e.target.value)}
                slotProps={{ htmlInput: { 'aria-label': t('中继配置串') } }}
              />
              <Button variant="contained" disabled={config.trim() === ''} onClick={() => void add()}>
                {t('添加中继')}
              </Button>
            </Box>
          }
        />
      </SettingsCard>

      <SettingsCard title={t('监听端口')}>
        <SettingRow
          title={t('UDP 端口')}
          hint={portNote}
          below={
            settings && (
              <ChoiceField
                label={t('监听端口')}
                value={settings.listenPort}
                choices={PORTS}
                unit={t('端口号')}
                min={1}
                max={65535}
                onSave={(v) => save(() => Service.SetListenPort(v), ['settings'], t('已保存，重启 Portide 后生效'))}
              />
            )
          }
        />
      </SettingsCard>

      <SettingsCard title={t('本机地址')}>
        <SettingRow
          title={t('手动连接时填写')}
          hint={t('自动发现不工作时（例如不同网段），在对方的 portide join --addr 或设置里填写其中一个')}
          below={
            addrs.length === 0 ? (
              <Typography variant="body2" sx={{ color: 'portide.muted' }}>
                {t('没有找到局域网地址')}
              </Typography>
            ) : (
              <Box sx={{ display: 'flex', flexDirection: 'column', gap: 0.5 }}>
                {addrs.map((a) => (
                  <Box key={a} sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                    <Mono>{a}</Mono>
                    <Tooltip title={t('复制')}>
                      <IconButton size="small" aria-label={t('复制 {addr}', { addr: a })} onClick={() => void run(() => Clipboard.SetText(a), t('已复制'))}>
                        <Copy size={13} />
                      </IconButton>
                    </Tooltip>
                  </Box>
                ))}
              </Box>
            )
          }
        />
      </SettingsCard>

      <Dialog open={removing !== null} onClose={() => setRemoving(null)}>
        <DialogTitle>{t('删除中继 {addr}？', { addr: removing?.addr ?? '' })}</DialogTitle>
        <DialogContent>
          <DialogContentText>{t('删除会同步到设备圈里的所有设备；没有中继时只在局域网内连接，跨网络连不上其他设备。')}</DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button variant="tonal" onClick={() => setRemoving(null)}>
            {t('取消')}
          </Button>
          <Button
            variant="contained"
            color="error"
            onClick={() => {
              const id = removing?.id ?? '';
              setRemoving(null);
              void save(() => Service.RemoveRelay(id), ['relay'], t('已删除中继'));
            }}
          >
            {t('删除')}
          </Button>
        </DialogActions>
      </Dialog>
    </>
  );
}
