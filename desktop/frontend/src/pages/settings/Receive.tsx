// 设置 › 接收（M3c 设计 4.4）：保存位置（友好路径 + 完整路径，"打开""更改"）、接收方式（分段按钮）、
// 大文件阈值（不适用时变淡并写明原因）、同名文件的说明。
import { Box, Button, ToggleButton, ToggleButtonGroup, Typography } from '@mui/material';
import { FolderOpen } from 'lucide-react';
import { Platform, Service } from '../../api/portide';
import { useSettings } from '../../api/queries';
import { useRun } from '../../api/useRun';
import { fontMono } from '../../theme';
import { ChoiceField, friendlyPath, SettingRow, SettingsCard, useSave } from './parts';
import { t } from '../../i18n';

const MB = 1024 * 1024;
/** "大文件确认"默认的阈值。 */
export const DEFAULT_ASK_ABOVE = 100 * MB;
const THRESHOLDS = [
  { value: 10 * MB, label: '10 MB' },
  { value: 100 * MB, label: '100 MB' },
  { value: 1024 * MB, label: '1 GB' },
];

export function Receive() {
  const save = useSave();
  const run = useRun();
  const { data: s } = useSettings();
  if (!s) return null;
  const askAbove = s.askAbove > 0 ? s.askAbove : DEFAULT_ASK_ABOVE;
  const changeDir = async () => {
    const dir = await run(() => Platform.PickFolder(t('选择保存位置')));
    if (dir) await save(() => Service.SetSaveDir(dir));
  };
  return (
    <SettingsCard title={t('接收')} showTitle={false}>
      <SettingRow
        title={t('保存位置')}
        hint={
          <>
            <Box component="span" sx={{ color: 'portide.fg2', fontWeight: 500 }}>
              {friendlyPath(s.saveDir)}
            </Box>
            <Box component="span" sx={{ display: 'block', fontFamily: fontMono, fontSize: 12, wordBreak: 'break-all' }} title={s.saveDir}>
              {s.saveDir}
            </Box>
          </>
        }
        control={
          <Box sx={{ display: 'flex', gap: 1 }}>
            <Button size="small" variant="tonal" startIcon={<FolderOpen size={14} />} onClick={() => void run(() => Service.OpenSaveDir())}>
              {t('打开')}
            </Button>
            <Button size="small" variant="tonal" onClick={() => void changeDir()}>
              {t('更改')}
            </Button>
          </Box>
        }
      />
      <SettingRow
        title={t('接收方式')}
        hint={s.receiveMode === 'auto' ? t('收到的内容直接保存') : s.receiveMode === 'ask' ? t('每次都先问你，2 分钟内不确认就拒绝') : t('超过阈值的先问你，小的直接保存')}
        below={
          <ToggleButtonGroup
            exclusive
            aria-label={t('接收方式')}
            value={s.receiveMode}
            onChange={(_, mode: string | null) => mode && void save(() => Service.SetReceivePolicy(mode, mode === 'ask-above' ? askAbove : 0))}
          >
            <ToggleButton value="auto">{t('直接接收')}</ToggleButton>
            <ToggleButton value="ask">{t('每次确认')}</ToggleButton>
            <ToggleButton value="ask-above">{t('大文件确认')}</ToggleButton>
          </ToggleButtonGroup>
        }
      />
      <SettingRow
        title={t('大文件阈值')}
        hint={s.receiveMode === 'ask-above' ? t('超过这个大小的接收需要确认') : t('只在"大文件确认"时使用')}
        dim={s.receiveMode !== 'ask-above'}
        below={
          <ChoiceField
            label={t('大文件阈值')}
            value={askAbove}
            choices={THRESHOLDS}
            unit="MB"
            scale={MB}
            min={1}
            max={1024 * 1024}
            disabled={s.receiveMode !== 'ask-above'}
            onSave={(v) => save(() => Service.SetReceivePolicy('ask-above', v))}
          />
        }
      />
      <SettingRow
        title={t('同名文件')}
        hint={
          <Typography variant="body2" component="span" sx={{ color: 'portide.muted' }}>
            {t('保存位置里已有同名文件时自动改名（例如"报告 (1).pdf"），不会覆盖。')}
          </Typography>
        }
      />
    </SettingsCard>
  );
}
