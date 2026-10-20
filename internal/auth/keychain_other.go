//go:build !darwin

package auth

import "fmt"

func SetSecret(key, value string) error {
	return fmt.Errorf("secure credential storage is currently supported on macOS only")
}
func GetSecret(key string) (string, error) {
	return "", fmt.Errorf("secure credential storage is currently supported on macOS only")
}
