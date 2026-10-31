package ssh

import (
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"errors"
	"hash"
	"math/big"
)

func newSHA256() hash.Hash { return sha256.New() }
func newSHA1() hash.Hash   { return sha1.New() }

// keyExchangeAlgorithm mirrors the C++ SSH::KeyExchange.  Only
// diffie-hellman-group-exchange with SHA-1/SHA-256 is implemented, as in
// the C++ original.
type keyExchangeAlgorithm interface {
	name() string
	hashFunc() func() hash.Hash
}

type dhGroupExchange struct {
	name_ string
	hash_ func() hash.Hash
}

func (k *dhGroupExchange) name() string               { return k.name_ }
func (k *dhGroupExchange) hashFunc() func() hash.Hash { return k.hash_ }

// keyExchangeAlgorithms mirrors KeyExchange::add_algorithms: SHA-256 is
// preferred over SHA-1.
func keyExchangeAlgorithms() []keyExchangeAlgorithm {
	return []keyExchangeAlgorithm{
		&dhGroupExchange{"diffie-hellman-group-exchange-sha256", newSHA256},
		&dhGroupExchange{"diffie-hellman-group-exchange-sha1", newSHA1},
	}
}

func findKeyExchange(name string) keyExchangeAlgorithm {
	for _, alg := range keyExchangeAlgorithms() {
		if alg.name() == name {
			return alg
		}
	}
	return nil
}

// testPrimeAndGenerator is the hardcoded DH group the C++ server always
// offers (USE_TEST_GROUP in ssh_key_exchange.cc): a 1024-bit prime with
// generator 2, SSH-encoded as mpint(p) || mpint(g).
var testPrimeAndGenerator = []byte{
	0x00, 0x00, 0x00, 0x81, 0x00, 0xe3, 0x1d, 0xfe, 0x85, 0x59, 0x9b, 0xcb, 0x5c, 0x2b, 0xbe, 0xcf,
	0x20, 0x1f, 0x5f, 0x49, 0xf1, 0xea, 0x31, 0x07, 0x7d, 0xa9, 0x26, 0xcb, 0x31, 0x03, 0x9d, 0x82,
	0x33, 0x2f, 0xed, 0x67, 0xa3, 0xa9, 0xb1, 0xc9, 0xe6, 0x34, 0x6c, 0xd7, 0xb5, 0x1a, 0x0a, 0x94,
	0x11, 0xa7, 0xd9, 0x26, 0xff, 0x0e, 0x8d, 0x72, 0xc1, 0x7b, 0x53, 0x9a, 0x13, 0x78, 0x7e, 0x16,
	0x38, 0x74, 0x7c, 0xb2, 0xdc, 0x60, 0x2c, 0x8c, 0xe8, 0x31, 0xf8, 0xd9, 0x7b, 0xac, 0xa6, 0x71,
	0xee, 0x61, 0x0c, 0x1a, 0xa4, 0x2f, 0x47, 0x2f, 0xe2, 0x22, 0xbd, 0x01, 0xe5, 0x25, 0xb6, 0x95,
	0xda, 0x3f, 0xf7, 0x03, 0xf4, 0x0e, 0xd6, 0x8c, 0xbb, 0x69, 0x1d, 0xcb, 0xd1, 0xe2, 0x60, 0xdb,
	0xf5, 0x0b, 0x85, 0x98, 0xe6, 0x17, 0xbe, 0x29, 0x4e, 0xa7, 0x90, 0x11, 0xac, 0xbc, 0xa5, 0x3e,
	0x05, 0xfe, 0xe9, 0x56, 0x93, 0x00, 0x00, 0x00, 0x01, 0x02,
}

// dhState carries one side of the group exchange.
type dhState struct {
	p, g   *big.Int
	x      *big.Int // private exponent
	pub    *big.Int // e (client) or f (server)
	shared *big.Int // K
}

func (d *dhState) generate() error {
	if d.p == nil || d.g == nil {
		return errors.New("ssh: DH group not set")
	}
	// x in [1, p-2]; OpenSSL's DH_generate_key draws a random private
	// key in a comparable range.
	limit := new(big.Int).Sub(d.p, big.NewInt(1))
	x, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return err
	}
	if x.Sign() == 0 {
		x.SetInt64(1)
	}
	d.x = x
	d.pub = new(big.Int).Exp(d.g, x, d.p)
	return nil
}

func (d *dhState) computeShared(peerPub *big.Int) error {
	upper := new(big.Int).Sub(d.p, big.NewInt(1))
	if peerPub.Cmp(big.NewInt(1)) <= 0 || peerPub.Cmp(upper) >= 0 {
		return errors.New("ssh: peer DH public key out of range")
	}
	d.shared = new(big.Int).Exp(peerPub, d.x, d.p)
	if d.shared.Cmp(big.NewInt(1)) <= 0 {
		return errors.New("ssh: degenerate DH shared secret")
	}
	return nil
}

// exchangeHash computes H per RFC 4253 group exchange:
//
//	hash(V_C || V_S || I_C || I_S || K_S || kexAccum || mpint(K))
//
// where kexAccum is the concatenation of the group-exchange message
// bodies ([min,n,max] || p || g || e || f, all wire-encoded).
func exchangeHash(h func() hash.Hash, clientVersion, serverVersion, clientKexinit, serverKexinit, serverHostKeyBlob, kexAccum []byte, k *big.Int) []byte {
	var data []byte
	data = appendString(data, clientVersion)
	data = appendString(data, serverVersion)
	data = appendString(data, clientKexinit)
	data = appendString(data, serverKexinit)
	data = appendString(data, serverHostKeyBlob)
	data = append(data, kexAccum...)
	data = appendMPInt(data, k)
	sum := h()
	sum.Write(data)
	return sum.Sum(nil)
}

// deriveKey implements the C++ Session::generate_key / RFC 4253 key
// derivation: KEY = HASH(K || H || letter || session_id), extended with
// HASH(K || H || KEY_so_far) until keySize bytes are available.  The
// shared secret K is used in its mpint wire encoding.
func deriveKey(h func() hash.Hash, sharedSecretMPInt, exchangeHash, sessionID []byte, letter byte, keySize int) []byte {
	var key []byte
	for len(key) < keySize {
		sum := h()
		sum.Write(sharedSecretMPInt)
		sum.Write(exchangeHash)
		if len(key) == 0 {
			sum.Write([]byte{letter})
			sum.Write(sessionID)
		} else {
			sum.Write(key)
		}
		key = append(key, sum.Sum(nil)...)
	}
	return key[:keySize]
}
