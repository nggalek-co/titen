package auth

import (
	"errors"
	"fmt"
	"runtime"
	"syscall"
	"unsafe"
)

const (
	credentialGeneric             = 1
	credentialPersistLocalMachine = 2
	credentialMaxBlobSize         = 2560
)

var (
	credentialDLL = syscall.NewLazyDLL("advapi32.dll")
	credWrite     = credentialDLL.NewProc("CredWriteW")
	credRead      = credentialDLL.NewProc("CredReadW")
	credDelete    = credentialDLL.NewProc("CredDeleteW")
	credFree      = credentialDLL.NewProc("CredFree")
)

type windowsCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        syscall.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         unsafe.Pointer
	TargetAlias        *uint16
	UserName           *uint16
}

func credentialTarget(key string) (*uint16, error) {
	if key == "" {
		return nil, fmt.Errorf("credential key is required")
	}
	target, err := syscall.UTF16PtrFromString("co.nggalek.titen/" + key)
	if err != nil {
		return nil, fmt.Errorf("invalid credential key: %w", err)
	}
	return target, nil
}

func SetSecret(key, value string) error {
	target, err := credentialTarget(key)
	if err != nil {
		return err
	}
	blob := []byte(value)
	if len(blob) > credentialMaxBlobSize {
		return fmt.Errorf("credential exceeds Windows limit of %d bytes", credentialMaxBlobSize)
	}
	credential := windowsCredential{Type: credentialGeneric, TargetName: target, Persist: credentialPersistLocalMachine, CredentialBlobSize: uint32(len(blob))}
	if len(blob) > 0 {
		credential.CredentialBlob = &blob[0]
	}
	result, _, callErr := credWrite.Call(uintptr(unsafe.Pointer(&credential)), 0)
	runtime.KeepAlive(target)
	runtime.KeepAlive(blob)
	if result == 0 {
		return fmt.Errorf("Credential Manager save failed: %w", callErr)
	}
	return nil
}

func GetSecret(key string) (string, error) {
	target, err := credentialTarget(key)
	if err != nil {
		return "", err
	}
	var credential *windowsCredential
	result, _, callErr := credRead.Call(uintptr(unsafe.Pointer(target)), credentialGeneric, 0, uintptr(unsafe.Pointer(&credential)))
	runtime.KeepAlive(target)
	if result == 0 {
		return "", fmt.Errorf("Credential Manager lookup failed: %w", callErr)
	}
	defer credFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.CredentialBlobSize == 0 {
		return "", nil
	}
	if credential.CredentialBlob == nil || credential.CredentialBlobSize > credentialMaxBlobSize {
		return "", fmt.Errorf("Credential Manager returned an invalid credential")
	}
	return string(unsafe.Slice(credential.CredentialBlob, int(credential.CredentialBlobSize))), nil
}

func DeleteSecret(key string) error {
	target, err := credentialTarget(key)
	if err != nil {
		return err
	}
	result, _, callErr := credDelete.Call(uintptr(unsafe.Pointer(target)), credentialGeneric, 0)
	runtime.KeepAlive(target)
	if result == 0 {
		if errors.Is(callErr, syscall.ERROR_NOT_FOUND) {
			return nil
		}
		return fmt.Errorf("Credential Manager delete failed: %w", callErr)
	}
	return nil
}
