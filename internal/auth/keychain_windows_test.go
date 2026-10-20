package auth

import (
	"crypto/rand"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"unsafe"
)

func TestCredentialManagerRejectsInvalidKeys(t *testing.T) {
	for _, key := range []string{"", "invalid\x00key"} {
		if err := SetSecret(key, "unused"); err == nil {
			t.Fatalf("accepted key %q", key)
		}
		if _, err := GetSecret(key); err == nil {
			t.Fatalf("accepted key %q", key)
		}
	}
}

func TestCredentialManagerRejectsOversizedSecret(t *testing.T) {
	if err := SetSecret("test", strings.Repeat("a", credentialMaxBlobSize+1)); err == nil {
		t.Fatal("accepted oversized credential")
	}
}

func TestCredentialABI(t *testing.T) {
	credential := windowsCredential{}
	if unsafe.Sizeof(uintptr(0)) == 8 && (unsafe.Sizeof(credential) != 80 || unsafe.Offsetof(credential.CredentialBlob) != 40 || unsafe.Offsetof(credential.UserName) != 72) {
		t.Fatalf("incorrect Windows CREDENTIALW layout, size=%d blob=%d user=%d", unsafe.Sizeof(credential), unsafe.Offsetof(credential.CredentialBlob), unsafe.Offsetof(credential.UserName))
	}
}

func TestCredentialManagerRoundTrip(t *testing.T) {
	if os.Getenv("TITEN_TEST_CREDENTIALS") != "1" {
		t.Skip("set TITEN_TEST_CREDENTIALS=1 on an ephemeral Windows test runner")
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		t.Fatal(err)
	}
	key := fmt.Sprintf("integration-test-%x", random)
	target, err := credentialTarget(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		result, _, err := credentialDLL.NewProc("CredDeleteW").Call(uintptr(unsafe.Pointer(target)), credentialGeneric, 0)
		runtime.KeepAlive(target)
		if result == 0 {
			t.Errorf("delete dummy credential: %v", err)
		}
	})
	for _, value := range []string{"dummy-token\x00日本語", "updated-token", "", strings.Repeat("x", credentialMaxBlobSize)} {
		if err := SetSecret(key, value); err != nil {
			t.Fatalf("save dummy credential: %v", err)
		}
		got, err := GetSecret(key)
		if err != nil {
			t.Fatalf("read dummy credential: %v", err)
		}
		if got != value {
			t.Fatal("dummy credential did not roundtrip")
		}
	}
}
