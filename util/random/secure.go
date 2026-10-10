package random

import (
	cryptorand "crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
)

const secureAlphaNum = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
const secureLowerNum = "0123456789abcdefghijklmnopqrstuvwxyz"

func SecureSeq(n int) (string, error) {
	return secureSeqFromAlphabet(n, secureAlphaNum)
}

func SecureLowerSeq(n int) (string, error) {
	return secureSeqFromAlphabet(n, secureLowerNum)
}

func SecureToken(n int) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("secure token byte length must be positive")
	}
	b := make([]byte, n)
	if _, err := cryptorand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func secureSeqFromAlphabet(n int, alphabet string) (string, error) {
	if n <= 0 {
		return "", fmt.Errorf("secure sequence length must be positive")
	}
	if len(alphabet) == 0 {
		return "", fmt.Errorf("secure sequence alphabet must not be empty")
	}
	result := make([]byte, n)
	max := big.NewInt(int64(len(alphabet)))
	for i := range result {
		idx, err := cryptorand.Int(cryptorand.Reader, max)
		if err != nil {
			return "", err
		}
		result[i] = alphabet[idx.Int64()]
	}
	return string(result), nil
}
