// 搜索（M3c 设计 4.1）：Ctrl+K 或列表栏顶部打开；搜文字内容和文件名，结果从新到旧，标出设备；
// 上下键选择，Enter 或点击跳到对应会话并高亮那一条；Esc 关闭。
import { useEffect, useState, type KeyboardEvent } from 'react';
import { Box, ButtonBase, Dialog, InputBase, Typography } from '@mui/material';
import { Search } from 'lucide-react';
import type { SearchResult } from '../../api/portide';
import { useSearch } from '../../api/queries';
import { FileTypeIcon } from '../../components/FileTypeIcon';
import { formatListTime } from '../../format';
import { fileKind } from '../../icons';
import { useNav } from '../../store/nav';
import { t } from '../../i18n';

/** 输入停下来多久之后才搜索。 */
export const SEARCH_DEBOUNCE_MS = 200;

/** 把片段里匹配的部分（不区分大小写）标出来。 */
export function markMatch(text: string, query: string): { text: string; hit: boolean }[] {
  const q = query.trim().toLowerCase();
  if (!q) return [{ text, hit: false }];
  const out: { text: string; hit: boolean }[] = [];
  const lower = text.toLowerCase();
  let i = 0;
  for (let j = lower.indexOf(q); j >= 0 && lower.length === text.length; j = lower.indexOf(q, i)) {
    if (j > i) out.push({ text: text.slice(i, j), hit: false });
    out.push({ text: text.slice(j, j + q.length), hit: true });
    i = j + q.length;
  }
  if (i < text.length) out.push({ text: text.slice(i), hit: false });
  return out;
}

export function SearchDialog() {
  const nav = useNav();
  const [input, setInput] = useState('');
  const [query, setQuery] = useState('');
  const [sel, setSel] = useState(0);
  useEffect(() => {
    const t = window.setTimeout(() => setQuery(input), SEARCH_DEBOUNCE_MS);
    return () => window.clearTimeout(t);
  }, [input]);
  const { data: results = [], isFetching } = useSearch(query);
  useEffect(() => {
    setSel(0);
  }, [query]);

  const close = () => {
    nav.setSearchOpen(false);
    setInput('');
    setQuery('');
  };
  const open = (r: SearchResult) => {
    nav.openConversation(r.peerKey, r.historyId);
    close();
  };
  const onKeyDown = (e: KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setSel((s) => Math.min(s + 1, results.length - 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setSel((s) => Math.max(s - 1, 0));
      // isComposing 在 WebKit 上不可靠：确认输入法候选词的 Enter 有时 isComposing 为 false，但 keyCode 仍是 229。
    } else if (e.key === 'Enter' && !e.nativeEvent.isComposing && e.nativeEvent.keyCode !== 229 && results[sel]) {
      e.preventDefault();
      open(results[sel]);
    }
  };

  return (
    <Dialog open={nav.searchOpen} onClose={close} fullWidth maxWidth="sm" slotProps={{ paper: { sx: { alignSelf: 'flex-start', mt: '12vh' } } }}>
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.25, px: 2, py: 1.5, borderBottom: 1, borderColor: 'portide.line' }}>
        <Search size={18} />
        <InputBase
          autoFocus
          fullWidth
          value={input}
          onChange={(e) => setInput(e.target.value)}
          onKeyDown={onKeyDown}
          placeholder={t('搜索消息和文件名')}
          inputProps={{ 'aria-label': t('搜索消息和文件名') }}
          sx={{ fontSize: 15 }}
        />
      </Box>
      <Box role="listbox" aria-label={t('搜索结果')} sx={{ maxHeight: '50vh', overflowY: 'auto', p: 0.75 }}>
        {query.trim() === '' && (
          <Typography variant="body2" sx={{ color: 'portide.muted', p: 2, textAlign: 'center' }}>
            {t('输入文字或文件名，Enter 跳到那一条')}
          </Typography>
        )}
        {query.trim() !== '' && !isFetching && results.length === 0 && (
          <Typography variant="body2" sx={{ color: 'portide.muted', p: 2, textAlign: 'center' }}>
            {t('没有找到「{query}」', { query: query.trim() })}
          </Typography>
        )}
        {results.map((r, i) => (
          <ButtonBase
            key={r.historyId}
            role="option"
            aria-selected={i === sel}
            onClick={() => open(r)}
            onMouseEnter={() => setSel(i)}
            sx={{ display: 'flex', width: '100%', justifyContent: 'flex-start', gap: 1.25, px: 1.25, py: 1, borderRadius: '9px', textAlign: 'left', bgcolor: i === sel ? 'portide.chip' : 'transparent' }}
          >
            <FileTypeIcon kind={r.fileIndex >= 0 ? fileKind(r.snippet) : 'message'} size={32} />
            <Box sx={{ flex: 1, minWidth: 0 }}>
              <Typography noWrap sx={{ fontSize: 13.5 }}>
                {markMatch(r.snippet, query).map((p, k) =>
                  p.hit ? (
                    <Box key={k} component="mark" sx={{ bgcolor: 'portide.accentSoft', color: 'portide.accent', borderRadius: '3px', px: 0.25 }}>
                      {p.text}
                    </Box>
                  ) : (
                    <span key={k}>{p.text}</span>
                  ),
                )}
              </Typography>
              <Typography variant="caption" sx={{ color: 'portide.muted' }}>
                {r.direction === 'out' ? t('发给「{name}」', { name: r.peerName }) : t('来自「{name}」', { name: r.peerName })}
              </Typography>
            </Box>
            <Typography variant="caption" sx={{ color: 'portide.muted', flexShrink: 0 }}>
              {formatListTime(r.at)}
            </Typography>
          </ButtonBase>
        ))}
      </Box>
    </Dialog>
  );
}
