// 导航状态（M3c 设计 2）：当前页面、选中的会话 / 设备 / 设置分类、要高亮的消息、搜索框和添加设备对话框是否打开。
// 放在一个 context 里，图标栏、列表栏、内容区、快捷键和搜索跳转都改它。
import { createContext, useContext, useMemo, useState, type ReactNode } from 'react';

export type Page = 'timeline' | 'transfers' | 'devices' | 'settings';
export type SettingsSection = 'general' | 'receive' | 'notify' | 'network' | 'storage' | 'appearance' | 'about';

export type PairingTab = 'show' | 'enter';

/** "全部设备"会话的键（和服务层的 Conversation.key 一致）。 */
export const ALL_DEVICES = '';

const CONVERSATION_KEY = 'portide.conversation';

export interface Nav {
  page: Page;
  conversation: string; // 选中的会话：设备 KeyID，或 ALL_DEVICES
  device: string; // 设备页选中的设备 KeyID；为空时选本机
  section: SettingsSection;
  highlight: number | null; // 搜索跳转后要高亮的历史编号
  searchOpen: boolean;
  pairingOpen: boolean; // "添加设备"对话框（设备页、会话列表、空状态都能打开它）
  pairingTab: PairingTab; // 对话框打开时先显示哪一页
  go: (page: Page) => void;
  openConversation: (key: string, highlight?: number) => void;
  openDevice: (keyId: string) => void;
  openSection: (section: SettingsSection) => void;
  setSearchOpen: (open: boolean) => void;
  setPairingOpen: (open: boolean, tab?: PairingTab) => void;
  clearHighlight: () => void;
}

const Ctx = createContext<Nav | null>(null);

export function useNav(): Nav {
  const nav = useContext(Ctx);
  if (!nav) throw new Error('useNav must be used inside NavProvider');
  return nav;
}

function loadConversation(): string {
  try {
    return localStorage.getItem(CONVERSATION_KEY) ?? ALL_DEVICES;
  } catch {
    return ALL_DEVICES;
  }
}

export function NavProvider({ children, initial }: { children: ReactNode; initial?: Partial<Pick<Nav, 'page' | 'conversation' | 'device' | 'section'>> }) {
  const [page, setPage] = useState<Page>(initial?.page ?? 'timeline');
  const [conversation, setConversation] = useState(initial?.conversation ?? loadConversation);
  const [device, setDevice] = useState(initial?.device ?? '');
  const [section, setSection] = useState<SettingsSection>(initial?.section ?? 'general');
  const [highlight, setHighlight] = useState<number | null>(null);
  const [searchOpen, setSearchOpen] = useState(false);
  const [pairingOpen, setPairingOpen] = useState(false);
  const [pairingTab, setPairingTab] = useState<PairingTab>('show');
  const nav = useMemo<Nav>(
    () => ({
      page, conversation, device, section, highlight, searchOpen, setSearchOpen, pairingOpen, pairingTab,
      setPairingOpen: (open, tab) => {
        if (open) setPairingTab(tab ?? 'show');
        setPairingOpen(open);
      },
      go: setPage,
      openConversation: (key, h) => {
        setPage('timeline');
        setConversation(key);
        setHighlight(h ?? null);
        try {
          localStorage.setItem(CONVERSATION_KEY, key);
        } catch {
          // 本地存储不可用：只是下次不记得选中的会话
        }
      },
      openDevice: (keyId) => {
        setPage('devices');
        setDevice(keyId);
      },
      openSection: (s) => {
        setPage('settings');
        setSection(s);
      },
      clearHighlight: () => setHighlight(null),
    }),
    [page, conversation, device, section, highlight, searchOpen, pairingOpen, pairingTab],
  );
  return <Ctx.Provider value={nav}>{children}</Ctx.Provider>;
}
