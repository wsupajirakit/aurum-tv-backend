package ops

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"io"
	"regexp"
)

const keyBytes = 32

var keyIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,32}$`)

// GenKey prints fresh secrets for .env: an HMAC key pair entry and an admin API key.
// Nothing is stored; copy the values into the server .env and the app .env yourself.
func GenKey(keyID string, out io.Writer) error {
	if !keyIDPattern.MatchString(keyID) {
		return fmt.Errorf("%w: key id may only contain A-Z a-z 0-9 _ - (max 32)", ErrConfig)
	}
	secret, err := randomKey()
	if err != nil {
		return err
	}
	admin, err := randomKey()
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "# backend .env\nHMAC_KEYS=%s:%s\nAPI_KEY=%s\n\n# app .env (signing key only — never the API key)\nEXPO_PUBLIC_HMAC_KEY_ID=%s\nEXPO_PUBLIC_HMAC_SECRET=%s\n",
		keyID, secret, admin, keyID, secret)
	return nil
}

func randomKey() (string, error) {
	b := make([]byte, keyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
