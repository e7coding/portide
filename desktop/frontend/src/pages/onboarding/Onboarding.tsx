// 首次启动（合并设备圈设计 9）：欢迎页 →（文件密钥库时）设置口令 → 设备名（默认"joy 的笔记本"，附一键选项），
// 提交后核心自动建一个只有本机的设备圈，直接进主界面；要加入别的设备在"添加设备"里做。
// 已经设置过口令的再次启动只显示"输入口令"。
import { useState, type FormEvent, type ReactNode } from 'react';
import { Alert, Box, Button, ButtonBase, TextField, Typography } from '@mui/material';
import { CloudOff, KeyRound, ShieldCheck, Wifi } from 'lucide-react';
import { AppMark } from '../../components/AppMark';
import { useQueryClient } from '@tanstack/react-query';
import { Service, type State } from '../../api/portide';
import { errorText } from '../../api/errors';
import { t } from '../../i18n';

/** 执行一步启动操作：成功或失败后都重新读取启动状态（有些失败要靠新状态切到出错页），失败时显示原因。 */
function useStep() {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const run = async (action: () => Promise<unknown>) => {
    setBusy(true);
    setError('');
    try {
      await action();
    } catch (err) {
      setError(errorText(err));
    } finally {
      await qc.invalidateQueries({ queryKey: ['state'] });
      setBusy(false);
    }
  };
  return { busy, error, run };
}

/** 全窗口居中的卡片；step 为 [第几步, 共几步] 时在顶部显示进度点。 */
export function Frame({ children, step, width = 460 }: { children: ReactNode; step?: [number, number]; width?: number }) {
  return (
    <Box sx={{ minHeight: '100vh', display: 'grid', placeItems: 'center', bgcolor: 'portide.feed', p: 3 }}>
      <Box sx={{ width, maxWidth: '100%', bgcolor: 'portide.bg', borderRadius: '16px', border: 1, borderColor: 'portide.line', boxShadow: 2, p: 4 }}>
        {step && (
          <Box aria-label={t('第 {step} 步，共 {total} 步', { step: step[0], total: step[1] })} sx={{ display: 'flex', gap: 0.75, mb: 3 }}>
            {Array.from({ length: step[1] }, (_, i) => (
              <Box key={i} sx={{ height: 4, flex: 1, borderRadius: 2, bgcolor: i < step[0] ? 'portide.accent' : 'portide.chip' }} />
            ))}
          </Box>
        )}
        {children}
      </Box>
    </Box>
  );
}

const Logo = ({ size = 56 }: { size?: number }) => (
  <Box sx={{ borderRadius: `${size * 0.234}px`, boxShadow: 2 }}>
    <AppMark size={size} />
  </Box>
);

const POINTS = [
  { icon: Wifi, get title() { return t('局域网直连'); }, get text() { return t('同一个网络里不经过任何服务器，速度只取决于你的网'); } },
  { icon: ShieldCheck, get title() { return t('端到端加密'); }, get text() { return t('只有你自己的设备能解开，中继也看不到内容'); } },
  { icon: CloudOff, get title() { return t('离线也能发'); }, get text() { return t('对方不在线时先排队，它一上线就自动送达'); } },
];

function Welcome({ onStart }: { onStart: () => void }) {
  return (
    <Frame width={520}>
      <Box sx={{ display: 'flex', flexDirection: 'column', alignItems: 'center', textAlign: 'center' }}>
        <Logo size={64} />
        <Typography variant="h1" sx={{ mt: 2 }}>
          {t('Portide 摆渡')}
        </Typography>
        <Typography sx={{ color: 'portide.muted', mt: 0.5 }}>{t('在你自己的电脑和手机之间，安全地互传文件和文字')}</Typography>
      </Box>
      <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1.5, my: 3.5 }}>
        {POINTS.map((p) => (
          <Box key={p.title} sx={{ display: 'flex', gap: 1.5, alignItems: 'flex-start' }}>
            <Box sx={{ width: 36, height: 36, borderRadius: '10px', display: 'grid', placeItems: 'center', bgcolor: 'portide.accentSoft', color: 'portide.accent', flexShrink: 0 }}>
              <p.icon size={18} />
            </Box>
            <Box>
              <Typography sx={{ fontWeight: 600 }}>{p.title}</Typography>
              <Typography variant="body2" sx={{ color: 'portide.muted' }}>
                {p.text}
              </Typography>
            </Box>
          </Box>
        ))}
      </Box>
      <Button fullWidth size="large" variant="contained" onClick={onStart}>
        {t('开始使用')}
      </Button>
    </Frame>
  );
}

function Unlock({ state, step }: { state: State; step?: [number, number] }) {
  const { busy, error, run } = useStep();
  const [pass, setPass] = useState('');
  const [again, setAgain] = useState('');
  const first = state.firstPassphrase;
  const mismatch = first && again !== '' && again !== pass;
  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (pass === '' || mismatch || (first && again !== pass)) return;
    void run(() => Service.Unlock(pass));
  };
  return (
    <Frame step={step}>
      <Box component="form" onSubmit={submit} sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
        <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.25 }}>
          <Box sx={{ color: 'portide.accent', display: 'grid' }}>
            <KeyRound size={22} />
          </Box>
          <Typography variant="h2">{first ? t('设置口令') : t('输入口令')}</Typography>
        </Box>
        <Typography sx={{ color: 'portide.muted' }}>
          {first
            ? t('这台电脑没有可用的系统密钥库，Portide 的密钥会加密保存在数据目录里。请设置一个口令，以后每次启动都要输入它。')
            : t('请输入启动 Portide 的口令。')}
        </Typography>
        {(error || state.error) && <Alert severity="error">{error || state.error}</Alert>}
        <TextField label={t('口令')} type="password" autoFocus value={pass} onChange={(e) => setPass(e.target.value)} size="medium" />
        {first && (
          <TextField
            label={t('再输入一次')}
            type="password"
            size="medium"
            value={again}
            error={mismatch}
            helperText={mismatch ? t('两次输入的口令不一样') : ' '}
            onChange={(e) => setAgain(e.target.value)}
          />
        )}
        <Button type="submit" size="large" variant="contained" disabled={busy || pass === '' || (first && again !== pass)}>
          {first ? t('设置并继续') : t('解锁')}
        </Button>
      </Box>
    </Frame>
  );
}

function NameStep({ state, step }: { state: State; step?: [number, number] }) {
  const { busy, error, run } = useStep();
  const options = state.nameSuggestions.length > 0 ? state.nameSuggestions : [state.defaultName].filter(Boolean);
  const [name, setName] = useState(options[0] ?? '');
  const submit = (e: FormEvent) => {
    e.preventDefault();
    void run(() => Service.Init(name.trim()));
  };
  return (
    <Frame step={step}>
      <Box component="form" onSubmit={submit} sx={{ display: 'flex', flexDirection: 'column', gap: 2 }}>
        <Typography variant="h2">{t('给这台设备起个名字')}</Typography>
        <Typography sx={{ color: 'portide.muted' }}>{t('设备圈里的其他设备会看到它，以后也可以改。')}</Typography>
        {error && <Alert severity="error">{error}</Alert>}
        <TextField label={t('设备名')} autoFocus size="medium" value={name} onChange={(e) => setName(e.target.value)} slotProps={{ htmlInput: { maxLength: 64 } }} />
        {options.length > 1 && (
          <Box sx={{ display: 'flex', gap: 1, flexWrap: 'wrap' }}>
            {options.map((o) => (
              <ButtonBase
                key={o}
                onClick={() => setName(o)}
                sx={{ px: 1.25, py: 0.5, borderRadius: '8px', fontSize: 13, bgcolor: name === o ? 'portide.accentSoft' : 'portide.chip', color: name === o ? 'portide.accent' : 'portide.fg2' }}
              >
                {o}
              </ButtonBase>
            ))}
          </Box>
        )}
        <Button type="submit" size="large" variant="contained" disabled={busy || name.trim() === ''}>
          {t('下一步')}
        </Button>
      </Box>
    </Frame>
  );
}

/** 首次启动的整个流程（locked 和 setup 阶段）。设备名提交后核心自动建好单机的设备圈，状态变成 running 就进主界面。 */
export function Onboarding({ state }: { state: State }) {
  // 欢迎页只在真正的第一次出现：第一次设置口令，或者还没有设备身份。
  const fresh = (state.phase === 'locked' && state.firstPassphrase) || (state.phase === 'setup' && state.needsName);
  const [started, setStarted] = useState(!fresh);
  // 步骤总数在流程开始时定下：要设置口令时 2 步，否则 1 步（1 步时不显示进度）。
  const [total] = useState(state.phase === 'locked' && state.firstPassphrase ? 2 : 1);
  if (!started) return <Welcome onStart={() => setStarted(true)} />;
  if (state.phase === 'locked') return <Unlock state={state} step={state.firstPassphrase ? [1, 2] : undefined} />;
  return <NameStep state={state} step={total > 1 ? [total, total] : undefined} />;
}
