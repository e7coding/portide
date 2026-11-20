//go:build tools

// 本文件只用来让 go.mod 保留 gomobile bind 所需的依赖，不参与正常构建。
package mobile

import _ "golang.org/x/mobile/bind"
