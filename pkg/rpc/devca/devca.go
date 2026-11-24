// Package devca makes throwaway certificate authorities and service certificates for local development and tests
// of GRPC_AUTH=mtls. Production certificates come from the platform (cert-manager, ACM PCA, Vault, a service mesh):
// this is not a PKI.
package devca

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"time"
)

// CA signs service certificates.
type CA struct {
	Cert *x509.Certificate
	Key  *ecdsa.PrivateKey
	PEM  []byte
}

func serial() *big.Int {
	n, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	return n
}

func keyPEM(k *ecdsa.PrivateKey) []byte {
	der, _ := x509.MarshalECPrivateKey(k)
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

// NewCA creates a self-signed CA valid for ttl.
func NewCA(name string, ttl time.Duration) (*CA, error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: name},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(ttl),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &k.PublicKey, k)
	if err != nil {
		return nil, err
	}
	c, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, err
	}
	return &CA{Cert: c, Key: k, PEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}, nil
}

// Issue returns a certificate and key (PEM) for the service called cn. The common name is the caller name the
// server sees; dns lists the host names the service is reached as (compose: the service name, localhost).
// The certificate serves both as server and as client certificate.
func (ca *CA) Issue(cn string, dns []string, ttl time.Duration) (certPEM, keyPEMOut []byte, err error) {
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: cn}, DNSNames: dns,
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(ttl),
		KeyUsage:    x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, ca.Cert, &k.PublicKey, ca.Key)
	if err != nil {
		return nil, nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), keyPEM(k), nil
}

// KeyPEM returns the CA private key (PEM), for writing the dev CA to disk.
func (ca *CA) KeyPEM() []byte { return keyPEM(ca.Key) }
