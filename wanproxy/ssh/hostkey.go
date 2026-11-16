package ssh

import (
	"bytes"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

func ValidateHostKey(key *rsa.PrivateKey) error {
	if key == nil || key.N == nil || key.N.BitLen() < 2048 {
		return errors.New("ssh: RSA host keys require at least 2048 bits")
	}
	return key.Validate()
}

func LoadRSAPrivateKeyPEM(data []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("ssh: expected one PEM private key")
	}
	var key *rsa.PrivateKey
	var err error
	switch block.Type {
	case "RSA PRIVATE KEY":
		key, err = x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		var parsed any
		parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
		if err == nil {
			var ok bool
			key, ok = parsed.(*rsa.PrivateKey)
			if !ok {
				return nil, errors.New("ssh: PKCS#8 key is not RSA")
			}
		}
	default:
		return nil, fmt.Errorf("ssh: unsupported PEM type %q", block.Type)
	}
	if err != nil {
		return nil, err
	}
	if err := ValidateHostKey(key); err != nil {
		return nil, err
	}
	return key, nil
}
