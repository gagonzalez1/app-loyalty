package appleidentity

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
)

var ErrCipher = errors.New("apple token encryption unavailable")

// AAD prevents token swapping between clients, token types and other purposes
// sharing the operational encryption key. Tokens never appear in exports.
func tokenAAD(clientID, kind string) []byte {
	return []byte("puntazo:apple-identity:v1\x00" + clientID + "\x00" + kind)
}
func tokenGCM(key []byte) (cipher.AEAD, error) {
	if len(key) != 32 {
		return nil, ErrCipher
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, ErrCipher
	}
	return cipher.NewGCM(block)
}
func EncryptToken(key []byte, clientID, kind, token string) (encrypted, nonce []byte, err error) {
	gcm, err := tokenGCM(key)
	if err != nil {
		return nil, nil, err
	}
	if clientID == "" || token == "" || (kind != "refresh_token" && kind != "access_token") {
		return nil, nil, ErrCipher
	}
	nonce = make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, nil, err
	}
	return gcm.Seal(nil, nonce, []byte(token), tokenAAD(clientID, kind)), nonce, nil
}
func DecryptToken(key []byte, clientID, kind string, encrypted, nonce []byte) (string, error) {
	gcm, err := tokenGCM(key)
	if err != nil {
		return "", err
	}
	if len(nonce) != gcm.NonceSize() {
		return "", ErrCipher
	}
	raw, err := gcm.Open(nil, nonce, encrypted, tokenAAD(clientID, kind))
	if err != nil {
		return "", ErrCipher
	}
	return string(raw), nil
}
