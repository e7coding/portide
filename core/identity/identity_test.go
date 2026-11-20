package identity

import (
	"bytes"
	"regexp"
	"testing"
)

func TestKeyIDFormat(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^[0-9A-F]{64}$`).MatchString(id.KeyID) {
		t.Fatalf("KeyID %q is not 64 uppercase hex chars", id.KeyID)
	}
}

func TestFromSeedRoundTrip(t *testing.T) {
	a, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	b, err := FromSeed(a.Seed())
	if err != nil {
		t.Fatal(err)
	}
	if a.KeyID != b.KeyID || !bytes.Equal(a.Public, b.Public) {
		t.Fatal("identity restored from seed differs")
	}
	if _, err := FromSeed([]byte("short")); err == nil {
		t.Fatal("expected error for a short seed")
	}
}

func TestCertificateCarriesDeviceKey(t *testing.T) {
	id, err := Generate()
	if err != nil {
		t.Fatal(err)
	}
	cert, err := id.Certificate()
	if err != nil {
		t.Fatal(err)
	}
	pub, err := PeerKey(cert.Certificate)
	if err != nil {
		t.Fatal(err)
	}
	if KeyIDOf(pub) != id.KeyID {
		t.Fatal("certificate public key does not match the identity")
	}
	if _, err := PeerKey(nil); err == nil {
		t.Fatal("expected error for an empty chain")
	}
}
