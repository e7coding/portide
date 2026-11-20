// 等待确认的接收（M3c 设计 4.1）：出现在对应会话顶部（按设备名匹配，待确认记录里只有名字），"全部设备"里显示全部；
// 接受 / 拒绝 + 倒计时。摘要由核心生成，已经带着大小。
import { useEffect, useState } from 'react';
import { Box, Button, Typography } from '@mui/material';
import { Inbox } from 'lucide-react';
import { useQueryClient } from '@tanstack/react-query';
import { Service } from '../../api/portide';
import { errorText } from '../../api/errors';
import { useInbox } from '../../api/queries';
import { formatCountdown } from '../../format';
import { useToast } from '../../store/toast';
import { t } from '../../i18n';

export function InboxCards({ peerName }: { peerName: string | null }) {
  const qc = useQueryClient();
  const toast = useToast();
  const { data: inbox = [] } = useInbox();
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    if (inbox.length === 0) return;
    const tick = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(tick);
  }, [inbox.length]);

  // 正在处理决定的条目：按钮先禁用，避免连点两次（第二次只会报"已经处理过"）。
  const [deciding, setDeciding] = useState<number[]>([]);
  const decide = async (id: number, accept: boolean) => {
    if (deciding.includes(id)) return;
    setDeciding((ids) => [...ids, id]);
    try {
      await Service.DecideInbox(id, accept);
    } catch (err) {
      toast.error(errorText(err));
    } finally {
      setDeciding((ids) => ids.filter((x) => x !== id));
    }
    void qc.invalidateQueries({ queryKey: ['inbox'] });
  };

  const live = inbox.filter((e) => e.expiresAt > now && (peerName === null || e.peerName === peerName));
  if (live.length === 0) return null;
  return (
    <Box sx={{ display: 'flex', flexDirection: 'column', gap: 1, px: 2.5, pt: 1.5 }}>
      {live.map((e) => (
        <Box
          key={e.id}
          role="group"
          aria-label={t('来自「{name}」的待确认接收', { name: e.peerName })}
          sx={{ display: 'flex', alignItems: 'center', gap: 1.5, p: 1.25, borderRadius: '12px', border: 1, borderColor: 'portide.accent', bgcolor: 'portide.accentSoft' }}
        >
          <Box sx={{ width: 34, height: 34, borderRadius: '10px', display: 'grid', placeItems: 'center', bgcolor: 'portide.bg', color: 'portide.accent', flexShrink: 0 }}>
            <Inbox size={18} />
          </Box>
          <Box sx={{ flex: 1, minWidth: 0 }}>
            <Typography sx={{ fontWeight: 600 }} noWrap>
              {t('「{name}」想发给你：{summary}', { name: e.peerName, summary: e.summary })}
            </Typography>
            <Typography variant="caption" sx={{ color: 'portide.muted' }}>
              {t('{time} 内确认，过时自动拒绝', { time: formatCountdown(e.expiresAt - now) })}
            </Typography>
          </Box>
          <Button size="small" variant="contained" disabled={deciding.includes(e.id)} onClick={() => void decide(e.id, true)}>
            {t('接受')}
          </Button>
          <Button size="small" variant="tonal" disabled={deciding.includes(e.id)} onClick={() => void decide(e.id, false)}>
            {t('拒绝')}
          </Button>
        </Box>
      ))}
    </Box>
  );
}
