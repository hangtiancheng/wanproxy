package ssh

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/rc4"
	"fmt"
)

type encryptionAlgorithm interface {
	name() string
	blockSize() int
	keySize() int
	ivSize() int
	newEncrypt(key, iv []byte) (cipherState, error)
	newDecrypt(key, iv []byte) (cipherState, error)
}

type cipherState interface {
	crypt(dst, src []byte)
}

type streamCipher struct{ s cipher.Stream }

func (c *streamCipher) crypt(dst, src []byte) { c.s.XORKeyStream(dst, src) }

type blockCipher struct {
	b cipher.BlockMode
}

func (c *blockCipher) crypt(dst, src []byte) { c.b.CryptBlocks(dst, src) }

type ctrCipher struct {
	name_    string
	block_   int
	key_     int
	iv_      int
	newBlock func(key []byte) (cipher.Block, error)
}

func (a *ctrCipher) name() string   { return a.name_ }
func (a *ctrCipher) blockSize() int { return a.block_ }
func (a *ctrCipher) keySize() int   { return a.key_ }
func (a *ctrCipher) ivSize() int    { return a.iv_ }

func (a *ctrCipher) newEncrypt(key, iv []byte) (cipherState, error) {
	block, err := a.newBlock(key)
	if err != nil {
		return nil, err
	}
	return &streamCipher{s: cipher.NewCTR(block, iv)}, nil
}

func (a *ctrCipher) newDecrypt(key, iv []byte) (cipherState, error) {
	return a.newEncrypt(key, iv)
}

type cbcCipher struct {
	name_    string
	block_   int
	key_     int
	iv_      int
	newBlock func(key []byte) (cipher.Block, error)
}

func (a *cbcCipher) name() string   { return a.name_ }
func (a *cbcCipher) blockSize() int { return a.block_ }
func (a *cbcCipher) keySize() int   { return a.key_ }
func (a *cbcCipher) ivSize() int    { return a.iv_ }

func (a *cbcCipher) newEncrypt(key, iv []byte) (cipherState, error) {
	block, err := a.newBlock(key)
	if err != nil {
		return nil, err
	}
	return &blockCipher{b: cipher.NewCBCEncrypter(block, iv)}, nil
}

func (a *cbcCipher) newDecrypt(key, iv []byte) (cipherState, error) {
	block, err := a.newBlock(key)
	if err != nil {
		return nil, err
	}
	return &blockCipher{b: cipher.NewCBCDecrypter(block, iv)}, nil
}

type arcfourCipher struct{}

func (a *arcfourCipher) name() string   { return "arcfour" }
func (a *arcfourCipher) blockSize() int { return 1 }
func (a *arcfourCipher) keySize() int   { return 16 }
func (a *arcfourCipher) ivSize() int    { return 0 }

func (a *arcfourCipher) newEncrypt(key, iv []byte) (cipherState, error) {
	c, err := rc4.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return &streamCipher{s: c}, nil
}

func (a *arcfourCipher) newDecrypt(key, iv []byte) (cipherState, error) {
	return a.newEncrypt(key, iv)
}

func encryptionAlgorithms() []encryptionAlgorithm {
	aesBlock := func(size int) func([]byte) (cipher.Block, error) {
		return func(key []byte) (cipher.Block, error) {
			if len(key) != size {
				return nil, fmt.Errorf("ssh: bad key size %d", len(key))
			}
			return aes.NewCipher(key)
		}
	}
	desBlock := func(key []byte) (cipher.Block, error) {
		if len(key) != 24 {
			return nil, fmt.Errorf("ssh: bad 3des key size %d", len(key))
		}
		return des.NewTripleDESCipher(key)
	}
	return []encryptionAlgorithm{
		&ctrCipher{"aes128-ctr", 1, 16, 16, aesBlock(16)},
		&cbcCipher{"aes128-cbc", 16, 16, 16, aesBlock(16)},
		&ctrCipher{"aes192-ctr", 1, 24, 16, aesBlock(24)},
		&cbcCipher{"aes192-cbc", 16, 24, 16, aesBlock(24)},
		&ctrCipher{"aes256-ctr", 1, 32, 16, aesBlock(32)},
		&cbcCipher{"aes256-cbc", 16, 32, 16, aesBlock(32)},
		&ctrCipher{"3des-ctr", 1, 24, 8, desBlock},
		&cbcCipher{"3des-cbc", 8, 24, 8, desBlock},
		&arcfourCipher{},
	}
}

func findEncryption(name string) encryptionAlgorithm {
	for _, alg := range encryptionAlgorithms() {
		if alg.name() == name {
			return alg
		}
	}
	return nil
}

func packetBlockSize(alg encryptionAlgorithm) int {
	if alg == nil {
		return 8
	}
	if alg.blockSize() < 8 {
		return 8
	}
	return alg.blockSize()
}
