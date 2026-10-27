//go:build linux

package auth

import (
	"crypto/rand"
	"fmt"
	"os"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("integration-test-%x", random)
}

func TestSecretServiceRoundTrip(t *testing.T) {
	if os.Getenv("TITEN_TEST_CREDENTIALS") != "1" {
		t.Skip("set TITEN_TEST_CREDENTIALS=1 on a Linux desktop with a Secret Service")
	}
	key := testKey(t)
	t.Cleanup(func() {
		if err := secretStore.Delete(key); err != nil {
			t.Errorf("delete dummy secret: %v", err)
		}
	})
	for _, value := range []string{"dummy-token\x00日本語", "updated-token", ""} {
		if err := SetSecret(key, value); err != nil {
			t.Fatalf("save dummy secret: %v", err)
		}
		got, err := GetSecret(key)
		if err != nil {
			t.Fatalf("read dummy secret: %v", err)
		}
		if got != value {
			t.Fatal("dummy secret did not roundtrip")
		}
	}
}

func TestMissingSecretIsEmpty(t *testing.T) {
	if os.Getenv("TITEN_TEST_CREDENTIALS") != "1" {
		t.Skip("set TITEN_TEST_CREDENTIALS=1 on a Linux desktop with a Secret Service")
	}
	got, err := GetSecret(testKey(t))
	if err != nil || got != "" {
		t.Fatalf("missing secret returned %q, %v", got, err)
	}
}

func TestSecretServiceDelete(t *testing.T) {
	if os.Getenv("TITEN_TEST_CREDENTIALS") != "1" {
		t.Skip("set TITEN_TEST_CREDENTIALS=1 on a Linux desktop with a Secret Service")
	}
	if err := DeleteSecret(testKey(t)); err != nil {
		t.Fatalf("delete missing secret: %v", err)
	}
	key := testKey(t)
	if err := SetSecret(key, "dummy-token"); err != nil {
		t.Fatalf("save dummy secret: %v", err)
	}
	if err := DeleteSecret(key); err != nil {
		t.Fatalf("delete dummy secret: %v", err)
	}
	got, err := GetSecret(key)
	if err != nil || got != "" {
		t.Fatalf("deleted secret returned %q, %v", got, err)
	}
	if err := DeleteSecret(key); err != nil {
		t.Fatalf("second delete: %v", err)
	}
}
