// 启动出错页（M3c 设计 4.6）：按原因给出中文说明和操作。端口被占用："重试""换一个端口启动"；
// 数据来自更新版本的 Portide：说明并给出数据目录；另一个进程在用这个数据目录：说明并"重试"。
// 原始错误折叠在"详细信息"里，方便反馈。
import { useState } from 'react';
import { Alert, Box, Button, ButtonBase, Collapse, Typography } from '@mui/material';
import { ChevronDown, ChevronRight, TriangleAlert } from 'lucide-react';
import { useQueryClient } from '@tanstack/react-query';
import { Service, type State } from '../../api/portide';
import { errorText } from '../../api/errors';
import { fontMono } from '../../theme';
import { Frame } from './Onboarding';
import { t } from '../../i18n';

interface Explain {
  title: string;
  text: string;
  causes?: string[];
}

function explain(state: State): Explain {
  if (state.phase === 'busy') {
    return { title: t('Portide 已经在运行'), text: state.error, causes: [t('命令行的 portide daemon 正在用同一个数据目录：先停掉它，再点"重试"')] };
  }
  switch (state.errorKind) {
    case 'port-in-use':
      return {
        title: t('端口被占用'),
        text: t('Portide 需要的网络端口已经被别的程序用了，所以没法开始接收。'),
        causes: [t('另一个 Portide 正在运行（包括命令行的 portide daemon）'), t('别的程序占用了这个端口')],
      };
    case 'newer-version':
      return { title: t('数据来自更新版本的 Portide'), text: t('这个数据目录被更新版本的 Portide 用过，当前版本读不了。请升级 Portide 后再打开。') };
  }
  return { title: t('Portide 无法启动'), text: state.error };
}

export function StartupProblem({ state }: { state: State }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [details, setDetails] = useState(false);
  const act = async (action: () => Promise<State>) => {
    setBusy(true);
    setError('');
    try {
      qc.setQueryData(['state'], await action());
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };
  const e = explain(state);
  const detail = state.detail || (state.phase === 'error' && state.errorKind !== '' ? state.error : '');
  return (
    <Frame width={500}>
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.25 }}>
          <Box sx={{ width: 40, height: 40, borderRadius: '11px', display: 'grid', placeItems: 'center', bgcolor: 'portide.warnSoft', color: 'portide.warn' }}>
            <TriangleAlert size={21} />
          </Box>
          <Typography variant="h2">{e.title}</Typography>
        </Box>
        <Typography sx={{ color: 'portide.fg2' }}>{e.text}</Typography>
        {e.causes && (
          <Box>
            <Typography variant="body2" sx={{ color: 'portide.muted', mb: 0.5 }}>
              {t('可能的原因：')}
            </Typography>
            <Box component="ul" sx={{ m: 0, pl: 2.5, color: 'portide.fg2', fontSize: 13.5 }}>
              {e.causes.map((c) => (
                <li key={c}>{c}</li>
              ))}
            </Box>
          </Box>
        )}
        {state.errorKind === 'newer-version' && (
          <Box sx={{ p: 1.25, borderRadius: '10px', bgcolor: 'portide.chip', fontFamily: fontMono, fontSize: 12.5, wordBreak: 'break-all' }}>{state.dataDir}</Box>
        )}
        {error && <Alert severity="error">{error}</Alert>}
        <Box sx={{ display: 'flex', gap: 1.5, justifyContent: 'flex-end' }}>
          <Button variant={state.errorKind === 'port-in-use' ? 'tonal' : 'contained'} disabled={busy} onClick={() => void act(() => Service.Retry())}>
            {t('重试')}
          </Button>
          {state.errorKind === 'port-in-use' && (
            <Button variant="contained" disabled={busy} onClick={() => void act(() => Service.UseAutomaticPort())}>
              {t('换一个端口启动')}
            </Button>
          )}
        </Box>
        {state.errorKind === 'port-in-use' && (
          <Typography variant="caption" sx={{ color: 'portide.muted' }}>
            {t('"换一个端口启动"会把端口改成自动选择。局域网里的设备照样能找到它；在别的设备上手动填过本机地址的，需要改成新地址。')}
          </Typography>
        )}
        {detail && (
          <Box>
            <ButtonBase onClick={() => setDetails(!details)} aria-expanded={details} sx={{ gap: 0.5, color: 'portide.muted', fontSize: 12.5, borderRadius: '6px' }}>
              {details ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
              {t('详细信息')}
            </ButtonBase>
            <Collapse in={details}>
              <Box sx={{ mt: 1, p: 1.25, borderRadius: '10px', bgcolor: 'portide.chip', fontFamily: fontMono, fontSize: 12, wordBreak: 'break-all', color: 'portide.fg2' }}>{detail}</Box>
            </Collapse>
          </Box>
        )}
      </Box>
    </Frame>
  );
}
