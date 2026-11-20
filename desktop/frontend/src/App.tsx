// 根组件：按启动阶段（State.phase）显示首次启动流程、出错页或主界面（M3a 设计 2.4、M3c 设计 2、4.5、4.6）。
import { Box, CircularProgress } from '@mui/material';
import { usePortideChanges } from './api/changes';
import { usePortideState } from './api/queries';
import { AppShell } from './components/shell/AppShell';
import { Onboarding } from './pages/onboarding/Onboarding';
import { StartupProblem } from './pages/onboarding/StartupProblem';
import { NavProvider } from './store/nav';
import { useLang } from './i18n';

// 切换语言时按语言重新挂载各页面（key），所有文字都换成新语言；导航状态在 NavProvider 里，不受影响。
export function App() {
  usePortideChanges();
  const lang = useLang();
  const { data: state } = usePortideState();
  if (!state) {
    return (
      <Box sx={{ height: '100vh', display: 'grid', placeItems: 'center' }}>
        <CircularProgress size={28} />
      </Box>
    );
  }
  if (state.phase === 'running') {
    return (
      <NavProvider>
        <AppShell key={lang} state={state} />
      </NavProvider>
    );
  }
  // 解锁和首次启动是同一个流程：放在同一个位置，从"设置口令"进入"设备名"时组件不重新挂载（进度不丢）。
  if (state.phase === 'locked' || state.phase === 'setup') return <Onboarding key={lang} state={state} />;
  return <StartupProblem key={lang} state={state} />;
}
