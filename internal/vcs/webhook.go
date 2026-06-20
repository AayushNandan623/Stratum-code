// Webhook signature validation. HMAC-SHA256 over the raw request body, compared
// in constant time. Supports the GitHub "sha256=" prefix convention.
package vcs

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// ValidateSignature computes HMAC-SHA256(body, secret) and compares it to the
// provided signature in constant time. The signature may carry a "sha256="
// prefix (GitHub's X-Hub-Signature-256 format).
func ValidateSignature(body []byte, secret, signature string) error {
	sig := strings.TrimSpace(signature)
	sig = strings.TrimPrefix(sig, "sha256=")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := hex.EncodeToString(mac.Sum(nil))
	if !hmac.Equal([]byte(sig), []byte(expected)) {
		return ErrInvalidSignature
	}
	return nil
}
