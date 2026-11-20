// 每个页面的列表栏和内容区（M3c 设计 2）：传输页没有列表栏，内容占满。
import type { ComponentType } from 'react';
import type { State } from '../api/portide';
import type { Page } from '../store/nav';
import { Transfers } from './Transfers';
import { DeviceDetail } from './devices/DeviceDetail';
import { DeviceList } from './devices/DeviceList';
import { SettingsNav } from './settings/SettingsNav';
import { SettingsPage } from './settings/SettingsPage';
import { ConversationList } from './timeline/ConversationList';
import { ConversationView } from './timeline/ConversationView';

export interface PageView {
  List?: ComponentType;
  Content: ComponentType<{ state: State }>;
}

export const PAGES: Record<Page, PageView> = {
  timeline: { List: ConversationList, Content: ConversationView },
  transfers: { Content: Transfers },
  devices: { List: DeviceList, Content: DeviceDetail },
  settings: { List: SettingsNav, Content: SettingsPage },
};
