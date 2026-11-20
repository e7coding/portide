// Go 端返回的错误在 Wails 里是一个带 message 的对象（或字符串）；统一取出给用户看的那句话。
import { t } from '../i18n';
export function errorText(err: unknown): string {
  if (typeof err === 'string') return err;
  if (err && typeof err === 'object' && 'message' in err) return String((err as { message: unknown }).message);
  return t('出了点问题，请重试');
}
