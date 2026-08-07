package cjws

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
)

// PrivateKeyPEMType is the PEM block type of a PKCS#8 private key — what the
// credential file carries (§4.6) and what `catlogctl keygen` writes.
const PrivateKeyPEMType = "PRIVATE KEY"

// MarshalPrivateKeyPEM encodes a P-256 private key as PKCS#8 PEM (§4.6).
func MarshalPrivateKeyPEM(key *ecdsa.PrivateKey) (string, error) {
	if key == nil {
		return "", errors.New("cjws: nil private key")
	}
	if key.Curve != elliptic.P256() {
		return "", ErrBadCurve
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", fmt.Errorf("cjws: marshal private key: %w", err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: PrivateKeyPEMType, Bytes: der})), nil
}

// ParsePrivateKeyPEM decodes a P-256 private key from PKCS#8 or SEC1 PEM.
//
// This is the credential-file loader's first step (§4.6): parse the key, derive
// its thumbprint, and refuse to ship if that thumbprint is not the license's
// `cnf.jkt`.
func ParsePrivateKeyPEM(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("cjws: not PEM")
	}

	var (
		key any
		err error
	)
	switch block.Type {
	case PrivateKeyPEMType:
		key, err = x509.ParsePKCS8PrivateKey(block.Bytes)
	case "EC PRIVATE KEY":
		key, err = x509.ParseECPrivateKey(block.Bytes)
	default:
		return nil, fmt.Errorf("cjws: PEM type %q, want %q", block.Type, PrivateKeyPEMType)
	}
	if err != nil {
		return nil, fmt.Errorf("cjws: parse private key: %w", err)
	}

	ec, ok := key.(*ecdsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("cjws: PEM holds a %T, want an EC key", key)
	}
	if ec.Curve != elliptic.P256() {
		return nil, ErrBadCurve
	}
	return ec, nil
}
