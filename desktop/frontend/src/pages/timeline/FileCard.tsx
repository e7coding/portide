// 消息里的文件（M3c 设计 4.1）：单个文件是一张卡片（类型图标或缩略图、名字、大小、打开 / 在文件夹中显示）；
// 文件夹或多个文件合成一张卡片（"项目资料 · 9 个文件 · 1.0 MB"、前 3 个缩略图加"+N"），点开展开文件列表。
import { useState } from 'react';
import { Box, ButtonBase, IconButton, Tooltip, Typography } from '@mui/material';
import { ChevronDown, ChevronRight, ExternalLink, FolderOpen } from 'lucide-react';
import { Service, type TimelineFile, type TimelineItem } from '../../api/portide';
import { useThumbnail } from '../../api/queries';
import { useRun } from '../../api/useRun';
import { FileTypeIcon } from '../../components/FileTypeIcon';
import { fileKind, hasThumbnail } from '../../icons';
import { formatBytes } from '../../format';
import { t } from '../../i18n';

/** 文件夹卡片上最多显示几个缩略图。 */
export const FOLDER_THUMBS = 3;

const cardSx = {
  width: 300, maxWidth: '100%', borderRadius: '12px', border: 1, borderColor: 'portide.line', bgcolor: 'portide.bg', p: 1.25,
} as const;

function Thumb({ item, index, file, size }: { item: TimelineItem; index: number; file: TimelineFile; size: number }) {
  const want = file.exists && hasThumbnail(file.name);
  const { data: url } = useThumbnail(item.id, index, want);
  if (!url) return <FileTypeIcon kind={fileKind(file.name)} size={size} />;
  return <Box component="img" src={url} alt="" sx={{ width: size, height: size, borderRadius: `${Math.round(size * 0.2)}px`, objectFit: 'cover', flexShrink: 0, display: 'block' }} />;
}

function FileActions({ item, index, file }: { item: TimelineItem; index: number; file: TimelineFile }) {
  const run = useRun();
  if (!file.exists) return null;
  return (
    <Box sx={{ display: 'flex', gap: 0.25, flexShrink: 0 }}>
      <Tooltip title={t('打开')}>
        <IconButton size="small" aria-label={t('打开 {name}', { name: file.name })} onClick={() => void run(() => Service.OpenFile(item.id, index))}>
          <ExternalLink size={15} />
        </IconButton>
      </Tooltip>
      <Tooltip title={t('在文件夹中显示')}>
        <IconButton size="small" aria-label={t('在文件夹中显示 {name}', { name: file.name })} onClick={() => void run(() => Service.RevealFile(item.id, index))}>
          <FolderOpen size={15} />
        </IconButton>
      </Tooltip>
    </Box>
  );
}

const baseName = (name: string) => name.split('/').pop() ?? name;

export function FileCard({ item }: { item: TimelineItem }) {
  const file = item.files[0];
  return (
    <Box sx={{ ...cardSx, display: 'flex', alignItems: 'center', gap: 1.25 }} data-testid="file-card">
      <Thumb item={item} index={0} file={file} size={40} />
      <Box sx={{ flex: 1, minWidth: 0 }}>
        <Typography noWrap title={file.name} sx={{ fontWeight: 500 }}>
          {baseName(file.name)}
        </Typography>
        <Typography variant="caption" sx={{ color: file.exists ? 'portide.muted' : 'portide.warn' }}>
          {formatBytes(file.size)}
          {!file.exists && t(' · 文件已被移走或删除')}
        </Typography>
      </Box>
      <FileActions item={item} index={0} file={file} />
    </Box>
  );
}

/** 多个文件的标题：都在同一个顶层文件夹里时是文件夹名，否则是"N 个文件"。 */
export function folderTitle(item: TimelineItem): string {
  return folderTop(item) || t('{n} 个文件', { n: item.fileCount });
}

/** 多个文件都在同一个顶层文件夹里时返回文件夹名，否则返回空串。 */
function folderTop(item: TimelineItem): string {
  const tops = new Set(item.files.map((f) => (f.name.includes('/') ? f.name.split('/')[0] : '')));
  const [top] = [...tops];
  return tops.size === 1 && top ? top : '';
}

export function FolderCard({ item }: { item: TimelineItem }) {
  const run = useRun();
  const [open, setOpen] = useState(false);
  const title = folderTitle(item);
  const isFolder = folderTop(item) !== '';
  const images = item.files.map((f, i) => ({ f, i })).filter(({ f }) => f.exists && hasThumbnail(f.name)).slice(0, FOLDER_THUMBS);
  const more = item.fileCount - images.length;
  return (
    <Box sx={cardSx} data-testid="folder-card">
      <Box sx={{ display: 'flex', alignItems: 'center', gap: 1.25 }}>
        <FileTypeIcon kind={isFolder ? 'folder' : 'other'} size={40} />
        <Box sx={{ flex: 1, minWidth: 0 }}>
          <Typography noWrap sx={{ fontWeight: 500 }}>
            {title}
          </Typography>
          <Typography variant="caption" sx={{ color: 'portide.muted' }}>
            {t('{n} 个文件', { n: item.fileCount })} · {formatBytes(item.bytesTotal)}
          </Typography>
        </Box>
        {item.files[0]?.exists && (
          <Tooltip title={t('在文件夹中显示')}>
            <IconButton size="small" aria-label={t('在文件夹中显示')} onClick={() => void run(() => Service.RevealFile(item.id, 0))}>
              <FolderOpen size={15} />
            </IconButton>
          </Tooltip>
        )}
      </Box>
      {images.length > 0 && (
        <Box sx={{ display: 'flex', gap: 0.5, mt: 1 }}>
          {images.map(({ f, i }) => (
            <Thumb key={i} item={item} index={i} file={f} size={44} />
          ))}
          {more > 0 && (
            <Box sx={{ width: 44, height: 44, borderRadius: '9px', bgcolor: 'portide.chip', color: 'portide.muted', display: 'grid', placeItems: 'center', fontSize: 12, fontWeight: 600 }}>
              +{more}
            </Box>
          )}
        </Box>
      )}
      <ButtonBase
        onClick={() => setOpen(!open)}
        aria-expanded={open}
        sx={{ mt: 1, gap: 0.5, color: 'portide.muted', fontSize: 12, borderRadius: '6px', px: 0.5, '&:hover': { color: 'portide.fg' } }}
      >
        {open ? <ChevronDown size={14} /> : <ChevronRight size={14} />}
        {open ? t('收起') : t('展开文件列表')}
      </ButtonBase>
      {open && (
        <Box sx={{ mt: 0.5 }}>
          {item.files.map((f, i) => (
            <Box key={i} sx={{ display: 'flex', alignItems: 'center', gap: 1, py: 0.5 }}>
              <FileTypeIcon kind={fileKind(f.name)} size={24} />
              <Typography variant="body2" noWrap title={f.name} sx={{ flex: 1, minWidth: 0, color: f.exists ? 'portide.fg' : 'portide.muted' }}>
                {f.name}
              </Typography>
              <Typography variant="caption" sx={{ color: 'portide.muted', flexShrink: 0 }}>
                {formatBytes(f.size)}
              </Typography>
              <FileActions item={item} index={i} file={f} />
            </Box>
          ))}
          {item.fileCount > item.files.length && (
            <Typography variant="caption" sx={{ color: 'portide.muted', display: 'block', pt: 0.5 }}>
              {t('还有 {n} 个文件，请在文件夹中查看', { n: item.fileCount - item.files.length })}
            </Typography>
          )}
        </Box>
      )}
    </Box>
  );
}
