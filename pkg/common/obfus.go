package common

import "encoding/base64"

const salt = "azowernasdfoia"

func Obfuscate(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s + salt))
}

func Deobfuscate(s string) (string, error) {
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return "", err
	}
	decodedStr := string(decoded)
	// Remove the salt from the end
	if len(decodedStr) < len(salt) {
		return "", err
	}
	return decodedStr[:len(decodedStr)-len(salt)], nil
}
