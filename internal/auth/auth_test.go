package auth

import "testing"

func TestArgon2idPasswordHash(t *testing.T) {
	hash, err := HashPassword("a password that is definitely long enough")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword(hash, "a password that is definitely long enough") {
		t.Fatal("correct password was rejected")
	}
	if VerifyPassword(hash, "not the password") {
		t.Fatal("incorrect password was accepted")
	}
}
