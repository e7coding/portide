// "关于"里显示的版本和开源许可（M3a 设计 3.3）。
// 版本号只写在 core/version/VERSION（Go 程序、构建脚本和 iOS 工程都读它），这里构建时嵌入。
import raw from '../../../core/version/VERSION?raw';

export const VERSION = raw.trim();

export const LICENSES: { name: string; license: string }[] = [
  { name: 'quic-go', license: 'MIT' },
  { name: 'modernc.org/sqlite', license: 'BSD-3-Clause' },
  { name: 'google.golang.org/protobuf', license: 'BSD-3-Clause' },
  { name: 'golang.org/x/crypto、net、sys、term', license: 'BSD-3-Clause' },
  { name: 'filippo.io/cpace', license: 'BSD-3-Clause' },
  { name: 'lukechampine.com/blake3', license: 'MIT' },
  { name: 'libp2p/zeroconf', license: 'MIT' },
  { name: 'zalando/go-keyring', license: 'MIT' },
  { name: 'Wails', license: 'MIT' },
  { name: 'React', license: 'MIT' },
  { name: 'Material UI', license: 'MIT' },
  { name: 'Emotion', license: 'MIT' },
  { name: 'TanStack Query', license: 'MIT' },
  { name: 'Lucide', license: 'ISC' },
];
