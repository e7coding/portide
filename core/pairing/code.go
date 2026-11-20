// Package pairing 实现用 8 位配对码把新设备加入设备圈（设计方案 3.3 方式 2）。
package pairing

import (
	"crypto/rand"
	"fmt"
	"math/big"
	"strings"
	"time"
)

const (
	// CodeDigits 是配对码位数。
	CodeDigits = 8
	// MaxAttempts 是一个配对码允许的失败次数，超过即作废。
	MaxAttempts = 3
	// CodeTTL 是配对码的有效期。
	CodeTTL = 5 * time.Minute
)

// NewCode 生成均匀分布的 8 位数字配对码。
func NewCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(100_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%08d", n.Int64()), nil
}

// FormatCode 把配对码格式化为 "1234-5678"，方便人读。
func FormatCode(code string) string {
	if len(code) != CodeDigits {
		return code
	}
	return code[:4] + "-" + code[4:]
}

// NormalizeCode 去掉用户输入中的空格和连字符，并校验为 8 位数字。
func NormalizeCode(input string) (string, error) {
	s := strings.NewReplacer("-", "", " ", "").Replace(strings.TrimSpace(input))
	if len(s) != CodeDigits {
		return "", fmt.Errorf("pairing: code must have %d digits", CodeDigits)
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("pairing: code must contain digits only")
		}
	}
	return s, nil
}
