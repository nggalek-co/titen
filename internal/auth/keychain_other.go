//go:build !darwin && !windows

package auth

import "fmt"

func SetSecret(key, value string) error {
	return fmt.Errorf("secure credential storage is currently supported on macOS and Windows")
}
func GetSecret(key string) (string, error) {
	return "", fmt.Errorf("secure credential storage is currently supported on macOS and Windows")
}
