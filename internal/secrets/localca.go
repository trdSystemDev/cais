package secrets

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"slices"
	"strings"
	"time"
)

// CA local do modo tls: selfsigned (teste fora da internet, onde o Let's
// Encrypt nao alcanca). O cais gera uma CA e com ela um certificado com SAN
// pra todos os nomes; quem testa confia na CA uma vez (docker e sistema).
//
// A CA tem name constraints: so assina nomes dentro dos dominios do teste.
// Instalada como confiavel, ela nao consegue validar google.com nem nada
// fora do dominio, mesmo que a chave vaze.
const (
	TLSCACert = "tls_ca_crt" // publico: e o que se instala como confiavel
	TLSCAKey  = "tls_ca_key"
	TLSCert   = "tls_cert"
	TLSKey    = "tls_key"
)

const (
	caValidity   = 10 * 365 * 24 * time.Hour
	leafValidity = 397 * 24 * time.Hour // limite aceito pelos navegadores
	renewBefore  = 30 * 24 * time.Hour
)

// EnsureLocalTLS garante a CA e o certificado pros hosts. O certificado e
// refeito quando falta algum host, quando vence em menos de 30 dias ou
// quando a CA muda; a CA so e refeita se nao cobre mais os dominios (ai
// quem testa precisa confiar nela de novo). Devolve true se o certificado
// mudou (o proxy precisa ser recriado pra carregar o novo).
func EnsureLocalTLS(s Store, hosts, domains []string, now time.Time) (changed bool, err error) {
	if len(hosts) == 0 {
		return false, errors.New("nenhum host pro certificado")
	}
	ca, caKey, caNew, err := ensureCA(s, domains, now)
	if err != nil {
		return false, err
	}
	if !caNew && leafOK(s, ca, hosts, now) {
		return false, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial(),
		Subject:      pkix.Name{CommonName: hosts[0]},
		DNSNames:     hosts,
		NotBefore:    now.Add(-time.Hour), // folga pra relogio adiantado
		NotAfter:     now.Add(leafValidity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
	if err != nil {
		return false, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return false, err
	}
	// chave antes do certificado: se cair no meio, o certificado antigo
	// nao bate com a chave nova e o proximo Setup refaz os dois
	if err := s.Put(TLSKey, pemBlock("PRIVATE KEY", keyDER)); err != nil {
		return false, err
	}
	if err := s.Put(TLSCert, pemBlock("CERTIFICATE", der)); err != nil {
		return false, err
	}
	return true, nil
}

func ensureCA(s Store, domains []string, now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, bool, error) {
	ca, key, err := loadCA(s)
	if err == nil && ca.NotAfter.After(now.Add(renewBefore)) && coversDomains(ca, domains) {
		return ca, key, false, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, nil, false, err
	}

	key, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, false, err
	}
	tmpl := &x509.Certificate{
		SerialNumber:                serial(),
		Subject:                     pkix.Name{CommonName: "cais CA local (" + strings.Join(domains, ", ") + ")"},
		NotBefore:                   now.Add(-time.Hour),
		NotAfter:                    now.Add(caValidity),
		KeyUsage:                    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid:       true,
		IsCA:                        true,
		MaxPathLenZero:              true,
		PermittedDNSDomainsCritical: true,
		PermittedDNSDomains:         domains,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, false, err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, nil, false, err
	}
	if err := s.Put(TLSCAKey, pemBlock("PRIVATE KEY", keyDER)); err != nil {
		return nil, nil, false, err
	}
	if err := s.Put(TLSCACert, pemBlock("CERTIFICATE", der)); err != nil {
		return nil, nil, false, err
	}
	ca, err = x509.ParseCertificate(der)
	return ca, key, true, err
}

func loadCA(s Store) (*x509.Certificate, *ecdsa.PrivateKey, error) {
	certPEM, err := s.Get(TLSCACert)
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := s.Get(TLSCAKey)
	if err != nil {
		return nil, nil, err
	}
	cert, err := parseCert(certPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", TLSCACert, err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("%s: PEM invalido", TLSCAKey)
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %w", TLSCAKey, err)
	}
	key, ok := k.(*ecdsa.PrivateKey)
	if !ok || !key.PublicKey.Equal(cert.PublicKey) {
		return nil, nil, fmt.Errorf("%s nao e a chave de %s", TLSCAKey, TLSCACert)
	}
	return cert, key, nil
}

// coversDomains diz se as name constraints da CA sao exatamente os dominios
// pedidos: a mais, a CA assinaria nomes que o teste nao usa mais.
func coversDomains(ca *x509.Certificate, domains []string) bool {
	have := slices.Clone(ca.PermittedDNSDomains)
	want := slices.Clone(domains)
	slices.Sort(have)
	slices.Sort(want)
	return slices.Equal(have, want)
}

// leafOK diz se o certificado atual serve: assinado por esta CA, cobre
// todos os hosts, a chave confere e nao esta perto de vencer.
func leafOK(s Store, ca *x509.Certificate, hosts []string, now time.Time) bool {
	certPEM, err := s.Get(TLSCert)
	if err != nil {
		return false
	}
	keyPEM, err := s.Get(TLSKey)
	if err != nil {
		return false
	}
	cert, err := parseCert(certPEM)
	if err != nil || cert.CheckSignatureFrom(ca) != nil || !cert.NotAfter.After(now.Add(renewBefore)) {
		return false
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return false
	}
	k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return false
	}
	if key, ok := k.(*ecdsa.PrivateKey); !ok || !key.PublicKey.Equal(cert.PublicKey) {
		return false
	}
	return slices.Equal(cert.DNSNames, hosts)
}

func parseCert(b []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("PEM de certificado invalido")
	}
	return x509.ParseCertificate(block.Bytes)
}

func pemBlock(typ string, der []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: der})
}

func serial() *big.Int {
	n, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		panic(err) // crypto/rand nao falha no Linux
	}
	return n
}
