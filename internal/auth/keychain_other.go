//go:build !darwin && !windows && !linux

package auth

import "fmt"

func SetSecret(key, value string) error {
	return fmt.Errorf("secure credential storage is currently supported on macOS, Windows, and Linux")
}
func GetSecret(key string) (string, error) {
	return "", fmt.Errorf("secure credential storage is currently supported on macOS, Windows, and Linux")
}
