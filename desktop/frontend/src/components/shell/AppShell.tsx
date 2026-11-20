// 主界面（M3c 设计 2）：图标栏 + 列表栏 + 内容区。这里还处理全局快捷键（Ctrl+1–4 切换页面、Ctrl+K 搜索）、
// 点了系统通知后切到时间线，以及拖放发送（整个窗口都是拖放目标，落点决定发给谁，见 store/drop.ts）。
import { useEffect, useRef } from 'react';
import { Box } from '@mui/material';
import { Events } from '@wailsio/runtime';
import { Service, type Device, type State } from '../../api/portide';
import { errorText } from '../../api/errors';
import { useDevices, useTransfers, useUpdate } from '../../api/queries';
import { dropTargets, parseDrop } from '../../store/drop';
import { useNav, type Page } from '../../store/nav';
import { useToast } from '../../store/toast';
import { PAGES } from '../../pages';
import { PairingDialog } from '../PairingDialog';
import { Rail, RAIL_ITEMS } from './Rail';
import { t } from '../../i18n';

export const FILES_DROPPED_EVENT = 'portide:files-dropped';
export const SHOW_TIMELINE_EVENT = 'portide:show-timeline';

export function AppShell({ state }: { state: State }) {
  const nav = useNav();
  const toast = useToast();
  const { data: transfers } = useTransfers();
  const { data: devices = [] } = useDevices();
  const { data: update } = useUpdate();

  // 事件只订阅一次：回调里通过 ref 读最新的设备列表、当前会话和提示函数。
  const latest = useRef({ devices, conversation: nav.conversation, toast, go: nav.go });
  latest.current = { devices, conversation: nav.conversation, toast, go: nav.go };
  useEffect(() => {
    const offDrop = Events.On(FILES_DROPPED_EVENT, (ev) => {
      const { devices: devs, conversation, toast: t } = latest.current;
      void sendDropped(parseDrop(ev.data), conversation, devs, t);
    });
    const offShow = Events.On(SHOW_TIMELINE_EVENT, () => latest.current.go('timeline'));
    return () => {
      offDrop();
      offShow();
    };
  }, []);

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (!(e.ctrlKey || e.metaKey) || e.altKey || e.shiftKey) return;
      const index = ['1', '2', '3', '4'].indexOf(e.key);
      if (index >= 0) {
        e.preventDefault();
        nav.go(RAIL_ITEMS[index].page);
      } else if (e.key.toLowerCase() === 'k') {
        e.preventDefault();
        nav.go('timeline');
        nav.setSearchOpen(true);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [nav]);

  const view = PAGES[nav.page];
  return (
    <Box data-file-drop-target sx={{ display: 'flex', height: '100vh', bgcolor: 'portide.bg', color: 'portide.fg' }}>
      <Rail page={nav.page} onChange={(p: Page) => nav.go(p)} active={transfers?.active ?? 0} update={update?.newer ?? false} />
      {view.List && <view.List />}
      <Box component="main" sx={{ flex: 1, minWidth: 0, display: 'flex', flexDirection: 'column', bgcolor: 'portide.bg' }}>
        <view.Content state={state} />
      </Box>
      <PairingDialog />
    </Box>
  );
}

/** 把拖进来的（或托盘里选的）文件发出去。 */
async function sendDropped(drop: ReturnType<typeof parseDrop>, conversation: string, devices: Device[], toast: ReturnType<typeof useToast>) {
  if (drop.paths.length === 0) return;
  const targets = dropTargets(drop, conversation, devices);
  if (targets.length === 0) {
    toast.warning(t('先在"全部设备"的发送区里选择要发给哪台设备，或者把文件拖到左侧的某台设备上'));
    return;
  }
  try {
    const res = await Service.SendFiles(targets, drop.paths);
    if (res.skipped.length > 0) toast.warning(t('跳过了 {n} 项（符号链接、特殊文件或无法读取）：{list}', { n: res.skipped.length, list: res.skipped.join(t('、')) }));
  } catch (err) {
    toast.error(errorText(err));
  }
}
