// 会话列表（M3c 设计 2、4.1）：顶部搜索框；第一项"全部设备"，然后每台设备一个会话：头像（带在线点）、名字、
// 最后一条内容、时间；离线设备上有排队的内容时显示"N 条等待对方上线"。每一行都是拖放目标：文件拖上去直接发给它。
import { Box, ButtonBase, IconButton, Tooltip, Typography } from '@mui/material';
import { Clock, Plus, Search } from 'lucide-react';
import type { Conversation } from '../../api/portide';
import { useConversations } from '../../api/queries';
import { DeviceAvatar } from '../../components/DeviceAvatar';
import { ListPane, ListRow } from '../../components/shell/ListPane';
import { formatListTime } from '../../format';
import { ALL_DROP } from '../../store/drop';
import { useNav } from '../../store/nav';
import { SearchDialog } from './SearchDialog';
import { t } from '../../i18n';

/** 会话行的第二行。 */
export function conversationPreview(c: Conversation, isAll: boolean): { text: string; queued: boolean } {
  if (c.queued > 0 && (isAll || !c.online)) return { text: t('{n} 条等待对方上线', { n: c.queued }), queued: true };
  if (!c.lastAt) return { text: t('还没有收发记录'), queued: false };
  return { text: c.lastDirection === 'out' && !isAll ? t('你：{text}', { text: c.lastPreview }) : c.lastPreview, queued: false };
}

export function ConversationList() {
  const nav = useNav();
  const { data: convs = [] } = useConversations();
  return (
    <ListPane
      title={t('时间线')}
      label={t('会话')}
      action={
        <Tooltip title={t('添加设备')}>
          <IconButton size="small" aria-label={t('添加设备')} onClick={() => nav.setPairingOpen(true)} sx={{ bgcolor: 'portide.chip' }}>
            <Plus size={16} />
          </IconButton>
        </Tooltip>
      }
    >
      <ButtonBase
        onClick={() => nav.setSearchOpen(true)}
        aria-label={t('搜索消息和文件')}
        sx={{ display: 'flex', justifyContent: 'flex-start', gap: 1, mx: 1.25, mb: 1, px: 1.25, py: 0.75, width: 'calc(100% - 20px)', borderRadius: '9px', bgcolor: 'portide.chip', color: 'portide.muted', fontSize: 13 }}
      >
        <Search size={15} />
        <span style={{ flex: 1, textAlign: 'left' }}>{t('搜索消息和文件')}</span>
        <Typography variant="caption" sx={{ color: 'portide.muted', opacity: 0.8 }}>
          Ctrl K
        </Typography>
      </ButtonBase>
      {convs.map((c) => {
        const isAll = c.key === '';
        const p = conversationPreview(c, isAll);
        const dropLabel = isAll ? t('松开发给发送区选中的设备') : c.online ? t('松开发给「{name}」', { name: c.name }) : t('松开发给「{name}」· 它上线后自动送达', { name: c.name });
        return (
          <ListRow
            key={c.key || 'all'}
            selected={nav.conversation === c.key}
            onClick={() => nav.openConversation(c.key)}
            label={isAll ? t('全部设备') : c.name}
            dropPeer={isAll ? ALL_DROP : c.key}
            dropLabel={dropLabel}
          >
            <DeviceAvatar icon={isAll ? 'all' : c.icon} online={isAll ? undefined : c.online} />
            <Box sx={{ flex: 1, minWidth: 0 }}>
              <Box sx={{ display: 'flex', alignItems: 'baseline', gap: 1 }}>
                <Typography noWrap sx={{ flex: 1, fontWeight: 600, fontSize: 13.5 }}>
                  {c.name}
                </Typography>
                <Typography variant="caption" sx={{ color: 'portide.muted', fontSize: 11, flexShrink: 0 }}>
                  {formatListTime(c.lastAt)}
                </Typography>
              </Box>
              <Typography noWrap variant="caption" component="div" sx={{ color: p.queued ? 'portide.warn' : 'portide.muted', display: 'flex', alignItems: 'center', gap: 0.5 }}>
                {p.queued && <Clock size={12} style={{ flexShrink: 0 }} />}
                <span style={{ overflow: 'hidden', textOverflow: 'ellipsis' }}>{p.text}</span>
              </Typography>
            </Box>
          </ListRow>
        );
      })}
      <SearchDialog />
    </ListPane>
  );
}
