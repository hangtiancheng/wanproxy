package ssh

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"hash"
)

type macAlgorithm interface {
	name() string
	size() int
	keySize() int
	newMAC(key []byte) hash.Hash
}

type stdMAC struct {
	name_   string
	size_   int
	key_    int
	newHash func() hash.Hash
}

func (m *stdMAC) name() string                { return m.name_ }
func (m *stdMAC) size() int                   { return m.size_ }
func (m *stdMAC) keySize() int                { return m.key_ }
func (m *stdMAC) newMAC(key []byte) hash.Hash { return hmac.New(m.newHash, key) }

func macAlgorithms() []macAlgorithm {
	return []macAlgorithm{
		&stdMAC{"hmac-sha1", 20, 20, sha1.New},
		&stdMAC{"hmac-sha2-256", 32, 32, sha256.New},
		&stdMAC{"hmac-sha2-512", 64, 64, sha512.New},
		&stdMAC{"hmac-md5", 16, 16, md5.New},
		&stdMAC{"hmac-sha1-96", 12, 20, sha1.New},
		&stdMAC{"hmac-md5-96", 12, 16, md5.New},
	}
}

func findMAC(name string) macAlgorithm {
	for _, alg := range macAlgorithms() {
		if alg.name() == name {
			return alg
		}
	}
	return nil
}
