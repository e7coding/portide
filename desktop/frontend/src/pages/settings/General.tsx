// 设置 › 通用（本机名字、关闭窗口时缩到托盘）和 通知与启动（系统通知、开机时启动、缩到托盘时提示）（M3c 设计 4.4）。
import { useEffect, useState } from 'react';
import { Box, Button, Switch, TextField } from '@mui/material';
import { Platform, Service, type State } from '../../api/portide';
import { useAutostart, usePrefs, useRenameCheck, useSettings } from '../../api/queries';
import { SettingRow, SettingsCard, useSave } from './parts';
import { renameBlockedText } from '../../format';
import { t } from '../../i18n';

export function General({ state }: { state: State }) {
  const save = useSave();
  const { data: prefs } = usePrefs();
  const { data: check } = useRenameCheck();
  const [name, setName] = useState(state.name);
  useEffect(() => {
    setName(state.name);
  }, [state.name]);
  const blocked = check && !check.allowed ? renameBlockedText(check.missing) : '';
  const rename = () =>
    void save(() => Service.RenameDevice(state.keyId, name.trim()), ['state', 'devices'], t('已改名，其他设备连上后会同步新名字'));
  return (
    <SettingsCard title={t('通用')} showTitle={false}>
      <SettingRow
        title={t('本机名字')}
        hint={blocked || t('其他设备上显示的名字，会同步给设备圈里的所有设备')}
        below={
          <Box sx={{ display: 'flex', gap: 1 }}>
            <TextField
              value={name}
              disabled={!check?.allowed}
              onChange={(e) => setName(e.target.value)}
              onKeyDown={(e) => e.key === 'Enter' && !e.nativeEvent.isComposing && name.trim() !== state.name && rename()}
              slotProps={{ htmlInput: { 'aria-label': t('本机名字'), maxLength: 64 } }}
              sx={{ width: 260 }}
            />
            <Button variant="tonal" disabled={!check?.allowed || name.trim() === '' || name.trim() === state.name} onClick={rename}>
              {t('改名')}
            </Button>
          </Box>
        }
      />
      <SettingRow
        title={t('关闭窗口时缩到托盘')}
        hint={prefs && !prefs.hasTray ? t('这台电脑的桌面没有托盘，关闭窗口即退出') : t('关掉后，关闭窗口就退出 Portide，不再接收文件')}
        dim={prefs ? !prefs.hasTray : false}
        control={
          <Switch
            checked={prefs?.closeToTray ?? true}
            disabled={!prefs?.hasTray}
            onChange={(e) => void save(() => Platform.SetCloseToTray(e.target.checked), ['prefs'])}
            slotProps={{ input: { 'aria-label': t('关闭窗口时缩到托盘') } }}
          />
        }
      />
    </SettingsCard>
  );
}

export function NotifyStartup() {
  const save = useSave();
  const { data: settings } = useSettings();
  const { data: autostart } = useAutostart();
  const { data: prefs } = usePrefs();
  return (
    <SettingsCard title={t('通知与启动')} showTitle={false}>
      <SettingRow
        title={t('系统通知')}
        hint={t('收到文件或文字、发送失败、有待确认的接收时提醒')}
        control={
          <Switch
            checked={settings?.notify ?? false}
            disabled={!settings}
            onChange={(e) => void save(() => Service.SetNotify(e.target.checked))}
            slotProps={{ input: { 'aria-label': t('系统通知') } }}
          />
        }
      />
      <SettingRow
        title={t('开机时启动')}
        hint={t('登录后在托盘里启动，不打开窗口')}
        control={
          <Switch
            checked={autostart ?? false}
            disabled={autostart === undefined}
            onChange={(e) => void save(() => Platform.SetAutostart(e.target.checked), ['autostart'])}
            slotProps={{ input: { 'aria-label': t('开机时启动') } }}
          />
        }
      />
      <SettingRow
        title={t('每次缩到托盘时提示')}
        hint={t('第一次缩到托盘时总会提示一次')}
        dim={prefs ? !prefs.hasTray : false}
        control={
          <Switch
            checked={prefs?.hideHint ?? false}
            disabled={!prefs?.hasTray}
            onChange={(e) => void save(() => Platform.SetHideHint(e.target.checked), ['prefs'])}
            slotProps={{ input: { 'aria-label': t('每次缩到托盘时提示') } }}
          />
        }
      />
    </SettingsCard>
  );
}
