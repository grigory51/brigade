package secret

import "testing"

func TestCipher(t *testing.T) {
	c := NewCipher("server-secret")

	// round-trip
	enc := c.Encrypt("ssh-key-material")
	if enc == "ssh-key-material" || enc == "" {
		t.Fatal("Encrypt не зашифровал")
	}
	if got := c.Decrypt(enc); got != "ssh-key-material" {
		t.Fatalf("round-trip: got %q", got)
	}

	// пустое — сентинел «не задано», не шифруется
	if c.Encrypt("") != "" {
		t.Fatal("пустое не должно шифроваться")
	}

	// legacy plaintext (без префикса) читается как есть
	if got := c.Decrypt("legacy-plain-token"); got != "legacy-plain-token" {
		t.Fatalf("legacy passthrough сломан: %q", got)
	}

	// чужой секрет не расшифровывает → fail-closed ""
	if got := NewCipher("other-secret").Decrypt(enc); got != "" {
		t.Fatalf("чужой секрет должен дать \"\", got %q", got)
	}
}
