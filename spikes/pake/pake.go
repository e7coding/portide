// Package pake 验证 filippo.io/cpace 用于 8 位配对码的可行性。M0 一次性验证代码。
package pake

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"filippo.io/cpace"
)

var associatedData = []byte("portide-pairing-v1")

func contextInfo(initiatorID, responderID string) *cpace.ContextInfo {
	return cpace.NewContextInfo(initiatorID, responderID, associatedData)
}

// Start 由发起方调用，得到发给响应方的 msgA。
func Start(code, initiatorID, responderID string) ([]byte, *cpace.State, error) {
	return cpace.Start(code, contextInfo(initiatorID, responderID))
}

// Respond 由响应方调用，得到回给发起方的 msgB 和共享密钥。
func Respond(code, initiatorID, responderID string, msgA []byte) (msgB, key []byte, err error) {
	return cpace.Exchange(code, contextInfo(initiatorID, responderID), msgA)
}

// ConfirmTag 派生确认标签。CPace 在配对码不一致时不会报错，只会得到不同的密钥，
// 所以双方必须互发并校验确认标签，才能判断配对码是否一致。
func ConfirmTag(key []byte, role string) ([]byte, error) {
	mk, err := hkdf.Expand(sha256.New, key, "portide-pairing-confirm", 32)
	if err != nil {
		return nil, err
	}
	m := hmac.New(sha256.New, mk)
	m.Write([]byte(role))
	return m.Sum(nil), nil
}

// SelfTest 在进程内完整跑一次交换并校验确认标签，返回耗时。供 iOS 真机调用。
func SelfTest() (string, error) {
	start := time.Now()
	msgA, st, err := Start("12345678", "initiator", "responder")
	if err != nil {
		return "", err
	}
	msgB, keyB, err := Respond("12345678", "initiator", "responder", msgA)
	if err != nil {
		return "", err
	}
	keyA, err := st.Finish(msgB)
	if err != nil {
		return "", err
	}
	tagA, err := ConfirmTag(keyA, "initiator")
	if err != nil {
		return "", err
	}
	tagB, err := ConfirmTag(keyB, "initiator")
	if err != nil {
		return "", err
	}
	if !hmac.Equal(tagA, tagB) {
		return "", errors.New("pake: confirm tags differ for the same code")
	}
	return fmt.Sprintf("cpace ok in %s (msgA=%dB msgB=%dB)", time.Since(start).Round(time.Microsecond), len(msgA), len(msgB)), nil
}
