// 设置 › 外观（浅色 / 深色 / 跟随系统，带小预览；界面语言）和 关于（图标、版本、开源许可、数据目录）（M3c 设计 4.4）。
import { Box, Button, ButtonBase, Switch, ToggleButton, ToggleButtonGroup, Typography } from '@mui/material';
import { useState } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { AppMark } from '../../components/AppMark';
import { Platform, type State } from '../../api/portide';
import { usePrefs, useUpdate } from '../../api/queries';
import { useRun } from '../../api/useRun';
import { LICENSES, VERSION } from '../../about';
import { fontMono, TOKENS } from '../../theme';
import { useThemeMode, type ThemeMode } from '../../store/themeMode';
import { SettingRow, SettingsCard, useSave } from './parts';
import { formatAgo } from '../../format';
import { normalizeLang, t, useSetLang, type LangSetting } from '../../i18n';

const MODES: { mode: ThemeMode; label: string }[] = [
  { mode: 'light', get label() { return t('浅色'); } },
  { mode: 'dark', get label() { return t('深色'); } },
  { mode: 'system', get label() { return t('跟随系统'); } },
];

/** 一个缩小的窗口：图标栏、列表栏、内容区和两个气泡。跟随系统时左浅右深。 */
function Preview({ mode }: { mode: ThemeMode }) {
  const half = (t: (typeof TOKENS)['light']) => (
    <Box sx={{ display: 'flex', flex: 1, bgcolor: t.bg }}>
      <Box sx={{ width: 10, bgcolor: t.rail }} />
      <Box sx={{ width: 22, bgcolor: t.list, borderRight: `1px solid ${t.line}` }} />
      <Box sx={{ flex: 1, bgcolor: t.feed, p: 0.75, display: 'flex', flexDirection: 'column', gap: 0.5 }}>
        <Box sx={{ width: '60%', height: 7, borderRadius: '3px', bgcolor: t.bg, border: `1px solid ${t.line}` }} />
        <Box sx={{ width: '45%', height: 7, borderRadius: '3px', bgcolor: t.accent, alignSelf: 'flex-end' }} />
      </Box>
    </Box>
  );
  return (
    <Box sx={{ display: 'flex', height: 58, borderRadius: '8px', overflow: 'hidden', border: 1, borderColor: 'portide.line' }}>
      {mode === 'dark' ? half(TOKENS.dark) : half(TOKENS.light)}
      {mode === 'system' && half(TOKENS.dark)}
    </Box>
  );
}

export function Appearance() {
  const { mode, setMode } = useThemeMode();
  return (
    <SettingsCard title={t('外观')} showTitle={false}>
      <Box role="radiogroup" aria-label={t('外观')} sx={{ display: 'grid', gridTemplateColumns: 'repeat(3, 1fr)', gap: 1.5, py: 1.5 }}>
        {MODES.map((m) => (
          <ButtonBase
            key={m.mode}
            role="radio"
            aria-checked={mode === m.mode}
            aria-label={m.label}
            onClick={() => setMode(m.mode)}
            sx={{
              display: 'block', textAlign: 'left', p: 1, borderRadius: '12px', border: 2,
              borderColor: mode === m.mode ? 'portide.accent' : 'transparent', bgcolor: mode === m.mode ? 'portide.accentSoft' : 'portide.list',
            }}
          >
            <Preview mode={m.mode} />
            <Typography variant="body2" sx={{ mt: 0.75, fontWeight: 500, color: mode === m.mode ? 'portide.accent' : 'portide.fg' }}>
              {m.label}
            </Typography>
          </ButtonBase>
        ))}
      </Box>
      <LanguageRow />
    </SettingsCard>
  );
}

/** 界面语言：跟随系统 / 中文 / English。语言名用各自的语言写，不翻译。托盘、通知和错误提示由 Go 端一起切换。 */
function LanguageRow() {
  const qc = useQueryClient();
  const run = useRun();
  const setLang = useSetLang();
  const { data: prefs } = usePrefs();
  const choose = (v: LangSetting | null) => {
    if (v === null || !prefs || v === prefs.language) return;
    void run(async () => {
      const next = await Platform.SetLanguage(v);
      qc.setQueryData(['prefs'], next);
      setLang(normalizeLang(next.uiLanguage));
      // 服务层的提示（中继状态、错误）按新语言重新取。
      void qc.invalidateQueries();
    });
  };
  return (
    <SettingRow
      title={t('语言')}
      hint={t('界面、托盘和通知使用的语言')}
      control={
        <ToggleButtonGroup exclusive aria-label={t('语言')} value={prefs?.language ?? ''} onChange={(_, v: LangSetting | null) => choose(v)}>
          <ToggleButton value="">{t('跟随系统')}</ToggleButton>
          <ToggleButton value="zh" lang="zh-CN">
            中文 {/* i18n-ignore */}
          </ToggleButton>
          <ToggleButton value="en" lang="en">
            English
          </ToggleButton>
        </ToggleButtonGroup>
      }
    />
  );
}

export function About({ state }: { state: State }) {
  return (
    <SettingsCard title={t('关于')} showTitle={false}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.5, py: 1.5 }}>
        <AppMark size={48} />
        <Box>
          <Typography variant="h3">{t('Portide 摆渡')}</Typography>
          <Typography variant="body2" sx={{ color: 'portide.muted' }}>
            {t('私人文件传输 · 版本 {version}', { version: VERSION })}
          </Typography>
        </Box>
      </Box>
      <UpdateRows />
      <Box sx={{ py: 1.25, borderTop: 1, borderColor: 'portide.line' }}>
        <Typography variant="body2" sx={{ color: 'portide.muted' }}>
          {t('数据目录')}
        </Typography>
        <Typography sx={{ fontFamily: fontMono, fontSize: 12.5, wordBreak: 'break-all' }}>{state.dataDir}</Typography>
      </Box>
      <Box sx={{ py: 1.25, borderTop: 1, borderColor: 'portide.line' }}>
        <Typography variant="body2" sx={{ color: 'portide.muted', mb: 0.5 }}>
          {t('使用的开源软件')}
        </Typography>
        <Box component="ul" sx={{ m: 0, pl: 2.5, columns: 2, fontSize: 13, color: 'portide.fg2' }}>
          {LICENSES.map((l) => (
            <li key={l.name}>
              {l.name}（{l.license}）
            </li>
          ))}
        </Box>
      </Box>
    </SettingsCard>
  );
}

/**
 * 检查更新：有新版本时显示版本号和"下载"（用浏览器打开发布页面）；自动检查的开关；"立即检查"。
 * 只提示，不自动下载安装。本地自己编译的版本（没有写入发布仓库）不显示这一块。
 */
function UpdateRows() {
  const qc = useQueryClient();
  const run = useRun();
  const save = useSave();
  const { data: prefs } = usePrefs();
  const { data: info } = useUpdate();
  const [checking, setChecking] = useState(false);
  if (!prefs?.canUpdate) return null;
  const checked = info && info.latest !== '' ? Date.parse(info.checkedAt) : NaN;
  const status = info?.newer
    ? t('有新版本 {version}', { version: info.latest })
    : Number.isNaN(checked)
      ? t('还没有检查过')
      : t('已是最新版本 · {ago}检查', { ago: formatAgo(checked) });
  const checkNow = () => {
    setChecking(true);
    void run(async () => {
      try {
        qc.setQueryData(['update'], await Platform.CheckUpdate(true));
      } finally {
        setChecking(false);
      }
    });
  };
  return (
    <>
      <SettingRow
        title={status}
        hint={info?.newer ? t('在浏览器里打开发布页面，下载安装后替换当前版本，数据和设置都会保留') : undefined}
        control={
          <Box sx={{ display: 'flex', gap: 1 }}>
            {info?.newer && (
              <Button variant="contained" onClick={() => void run(() => Platform.OpenURL(info.url))}>
                {t('下载')}
              </Button>
            )}
            <Button variant="tonal" disabled={checking} onClick={checkNow}>
              {checking ? t('正在检查…') : t('立即检查')}
            </Button>
          </Box>
        }
      />
      <SettingRow
        title={t('自动检查更新')}
        hint={t('每天向 GitHub 查询一次最新版本，GitHub 会看到这台电脑的 IP 地址。不会发送其他信息')}
        control={
          <Switch
            checked={prefs.updateCheck}
            onChange={(e) => void save(() => Platform.SetUpdateCheck(e.target.checked), ['prefs', 'update'])}
            slotProps={{ input: { 'aria-label': t('自动检查更新') } }}
          />
        }
      />
    </>
  );
}
