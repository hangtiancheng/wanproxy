package ssh

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
)

// hostKeyAlgorithm mirrors the C++ SSH::ServerHostKey.  Only ssh-rsa is
// supported, matching the C++ implementation (the client side registers
// RSAServerHostKey and the server side loads an RSA PEM key).
type hostKeyAlgorithm interface {
	name() string
	// encodePublicKey writes the public key blob (K_S content).
	encodePublicKey() []byte
	// sign signs the exchange hash (server side).
	sign(data []byte) ([]byte, error)
	// decodePublicKey imports a peer's public key blob (client side).
	decodePublicKey(blob []byte) error
	// verify verifies a signature over the exchange hash (client side).
	verify(signature, data []byte) error
}

type rsaHostKey struct {
	role Role
	priv *rsa.PrivateKey
	pub  *rsa.PublicKey
}

func (k *rsaHostKey) name() string { return "ssh-rsa" }

func (k *rsaHostKey) encodePublicKey() []byte {
	pub := k.pub
	if pub == nil {
		pub = &k.priv.PublicKey
	}
	var blob []byte
	blob = appendString(blob, []byte("ssh-rsa"))
	blob = appendMPInt(blob, big.NewInt(int64(pub.E)))
	blob = appendMPInt(blob, pub.N)
	return blob
}

func (k *rsaHostKey) sign(data []byte) ([]byte, error) {
	if k.priv == nil {
		return nil, errors.New("ssh: no private key for signing")
	}
	digest := sha1.Sum(data)
	sig, err := rsa.SignPKCS1v15(rand.Reader, k.priv, crypto.SHA1, digest[:])
	if err != nil {
		return nil, err
	}
	var out []byte
	out = appendString(out, []byte("ssh-rsa"))
	out = appendString(out, sig)
	return out, nil
}

func (k *rsaHostKey) decodePublicKey(blob []byte) error {
	tag, rest, err := readString(blob)
	if err != nil {
		return err
	}
	if string(tag) != "ssh-rsa" {
		return fmt.Errorf("ssh: unsupported host key type %q", tag)
	}
	eBig, rest, err := readMPInt(rest)
	if err != nil {
		return err
	}
	nBig, rest, err := readMPInt(rest)
	if err != nil {
		return err
	}
	if len(rest) != 0 {
		return errMalformed
	}
	if !eBig.IsInt64() {
		return errors.New("ssh: RSA exponent out of range")
	}
	k.pub = &rsa.PublicKey{N: nBig, E: int(eBig.Int64())}
	return nil
}

func (k *rsaHostKey) verify(signature, data []byte) error {
	if k.pub == nil {
		return errors.New("ssh: no public key for verification")
	}
	tag, rest, err := readString(signature)
	if err != nil {
		return err
	}
	if string(tag) != "ssh-rsa" {
		return fmt.Errorf("ssh: unsupported signature type %q", tag)
	}
	sig, _, err := readString(rest)
	if err != nil {
		return err
	}
	digest := sha1.Sum(data)
	return rsa.VerifyPKCS1v15(k.pub, crypto.SHA1, digest[:], sig)
}

// LoadRSAPrivateKeyPEM loads a PEM-encoded RSA private key, mirroring
// the C++ ServerHostKey::server() which reads PKCS#1 "RSA PRIVATE KEY"
// PEM files.  PKCS#8 keys are accepted as a convenience.
func LoadRSAPrivateKeyPEM(pemData []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(pemData)
	if block == nil {
		return nil, errors.New("ssh: no PEM block in host key file")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		return x509.ParsePKCS1PrivateKey(block.Bytes)
	case "PRIVATE KEY":
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		rsaKey, ok := key.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("ssh: PKCS#8 key is not RSA")
		}
		return rsaKey, nil
	}
	return nil, fmt.Errorf("ssh: unsupported PEM type %q", block.Type)
}

// serverHostKey builds the server-side key algorithm.
func serverHostKey(priv *rsa.PrivateKey) hostKeyAlgorithm {
	return &rsaHostKey{role: ServerRole, priv: priv}
}

// clientHostKey builds the client-side key algorithm (public key filled
// in when the server's REPLY is decoded).
func clientHostKey() hostKeyAlgorithm {
	return &rsaHostKey{role: ClientRole}
}

func findHostKey(name string, role Role, priv *rsa.PrivateKey) hostKeyAlgorithm {
	if name != "ssh-rsa" {
		return nil
	}
	if role == ServerRole {
		return serverHostKey(priv)
	}
	return clientHostKey()
}
