package credentials

import (
	"strings"
	"testing"
)

func TestCredentialCipherRoundTripAndRejectsWrongKey(t *testing.T) {
	t.Parallel()
	c, err := NewCipher("local-test-secret-key-material-please-change")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := c.Encrypt([]byte("zotero-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "zotero-api-key") {
		t.Fatal("ciphertext contains plaintext")
	}
	plain, err := c.Decrypt(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if string(plain) != "zotero-api-key" {
		t.Fatalf("plaintext=%q", plain)
	}

	other, err := NewCipher("different-secret-key-material")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.Decrypt(encoded); err == nil {
		t.Fatal("expected wrong key to fail")
	}
}
