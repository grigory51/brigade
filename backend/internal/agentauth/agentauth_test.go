package agentauth

import "testing"

func TestSignVerify(t *testing.T) {
	signer := NewSigner("test-secret")
	verifier, err := NewVerifier(signer.PublicKeyB64())
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}

	tok, err := signer.Token("sess-1")
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if err := verifier.Verify(tok, "sess-1"); err != nil {
		t.Fatalf("Verify valid: %v", err)
	}

	// Токен, выписанный для sess-1, не проходит для sess-2 (aud-привязка, anti-replay).
	if err := verifier.Verify(tok, "sess-2"); err == nil {
		t.Fatal("Verify accepted token for wrong session")
	}

	// Подмена символа в токене ломает подпись.
	bad := tok[:len(tok)-2] + "aa"
	if err := verifier.Verify(bad, "sess-1"); err == nil {
		t.Fatal("Verify accepted tampered token")
	}

	// Чужой публичный ключ не проверяет наш токен.
	other, _ := NewVerifier(NewSigner("other-secret").PublicKeyB64())
	if err := other.Verify(tok, "sess-1"); err == nil {
		t.Fatal("Verify accepted token under wrong key")
	}

	// Детерминизм: тот же секрет — тот же публичный ключ (демоны и рестарт brigade согласованы).
	if NewSigner("test-secret").PublicKeyB64() != signer.PublicKeyB64() {
		t.Fatal("public key not deterministic for same secret")
	}

	if _, err := NewVerifier("!!!not-base64"); err == nil {
		t.Fatal("NewVerifier accepted bad base64")
	}
}
