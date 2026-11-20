package pake

import (
	"bytes"
	"crypto/hmac"
	"testing"
)

func exchange(t *testing.T, codeA, codeB string) (keyA, keyB []byte) {
	t.Helper()
	msgA, st, err := Start(codeA, "laptop", "desktop")
	if err != nil {
		t.Fatal(err)
	}
	msgB, keyB, err := Respond(codeB, "laptop", "desktop", msgA)
	if err != nil {
		t.Fatal(err)
	}
	keyA, err = st.Finish(msgB)
	if err != nil {
		t.Fatal(err)
	}
	return keyA, keyB
}

func TestSameCodeSameKeyAndTagsVerify(t *testing.T) {
	keyA, keyB := exchange(t, "48213907", "48213907")
	if !bytes.Equal(keyA, keyB) {
		t.Fatal("same code must derive the same key")
	}
	tagA, _ := ConfirmTag(keyA, "initiator")
	tagB, _ := ConfirmTag(keyB, "initiator")
	if !hmac.Equal(tagA, tagB) {
		t.Fatal("confirm tags must match for the same code")
	}
}

func TestWrongCodeDetectedByConfirmTag(t *testing.T) {
	keyA, keyB := exchange(t, "48213907", "48213908")
	if bytes.Equal(keyA, keyB) {
		t.Fatal("different codes must derive different keys")
	}
	tagA, _ := ConfirmTag(keyA, "initiator")
	tagB, _ := ConfirmTag(keyB, "initiator")
	if hmac.Equal(tagA, tagB) {
		t.Fatal("confirm tags must differ for different codes")
	}
}

func TestSelfTest(t *testing.T) {
	res, err := SelfTest()
	if err != nil {
		t.Fatal(err)
	}
	t.Log(res)
}
