// 设置 › 存储与历史（M3c 设计 4.4）：历史保留（永久 / 30 天 / 90 天 / 1 年 / 自定义）、离线队列有效期（1 / 7 / 30 天）、
// 清空历史（二次确认）、数据目录（只显示）。
import { useState } from 'react';
import { Box, Button, Dialog, DialogActions, DialogContent, DialogContentText, DialogTitle } from '@mui/material';
import { Service, type State } from '../../api/portide';
import { useSettings } from '../../api/queries';
import { fontMono } from '../../theme';
import { useToast } from '../../store/toast';
import { ChoiceField, SettingRow, SettingsCard, useSave } from './parts';
import { t } from '../../i18n';

const HISTORY = [
  { value: 0, get label() { return t('永久'); } },
  { value: 30, get label() { return t('30 天'); } },
  { value: 90, get label() { return t('90 天'); } },
  { value: 365, get label() { return t('1 年'); } },
];
const QUEUE = [
  { value: 1, get label() { return t('1 天'); } },
  { value: 7, get label() { return t('7 天'); } },
  { value: 30, get label() { return t('30 天'); } },
];

export function Storage({ state }: { state: State }) {
  const save = useSave();
  const toast = useToast();
  const { data: s } = useSettings();
  const [confirm, setConfirm] = useState(false);
  return (
    <SettingsCard title={t('存储与历史')} showTitle={false}>
      {s && (
        <>
          <SettingRow
            title={t('历史保留')}
            hint={t('更早的收发记录会自动删除（只删记录，不删收到的文件）')}
            below={<ChoiceField label={t('历史保留')} value={s.historyDays} choices={HISTORY} unit={t('天')} min={1} max={3650} onSave={(v) => save(() => Service.SetHistoryDays(v))} />}
          />
          <SettingRow
            title={t('离线队列有效期')}
            hint={t('发给离线设备的内容最多等多久，过期就算失败')}
            below={<ChoiceField label={t('离线队列有效期')} value={s.queueDays} choices={QUEUE} unit={t('天')} min={1} max={365} onSave={(v) => save(() => Service.SetQueueDays(v))} />}
          />
        </>
      )}
      <SettingRow
        title={t('清空历史')}
        hint={t('删除本机的全部收发记录，已经收到的文件不受影响')}
        control={
          <Button size="small" color="error" variant="outlined" onClick={() => setConfirm(true)}>
            {t('清空')}
          </Button>
        }
      />
      <SettingRow
        title={t('数据目录')}
        hint={
          <Box component="span" sx={{ fontFamily: fontMono, fontSize: 12, wordBreak: 'break-all' }}>
            {state.dataDir}
          </Box>
        }
      />
      <Dialog open={confirm} onClose={() => setConfirm(false)}>
        <DialogTitle>{t('清空全部历史？')}</DialogTitle>
        <DialogContent>
          <DialogContentText>{t('本机的收发记录会全部删除，已经收到的文件不受影响。这个操作不能撤销。')}</DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button variant="tonal" onClick={() => setConfirm(false)}>
            {t('取消')}
          </Button>
          <Button
            variant="contained"
            color="error"
            onClick={() => {
              setConfirm(false);
              void save(
                async () => {
                  const n = await Service.ClearHistory();
                  toast.success(t('已清空 {n} 条历史记录', { n }));
                },
                ['timeline', 'conversations'],
                '',
              );
            }}
          >
            {t('清空')}
          </Button>
        </DialogActions>
      </Dialog>
    </SettingsCard>
  );
}
