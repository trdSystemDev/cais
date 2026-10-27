package secrets

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"testing"
	"time"
)

var (
	t0      = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	hosts   = []string{"trd.localhost", "*.trd.localhost", "registry.trd.localhost", "db.trd.localhost"}
	domains = []string{"trd.localhost"}
)

// verify confere a cadeia como um cliente TLS faria, confiando so na CA.
func verify(t *testing.T, s Store, name string, at time.Time) error {
	t.Helper()
	caPEM, _ := s.Get(TLSCACert)
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA ilegivel")
	}
	certPEM, _ := s.Get(TLSCert)
	keyPEM, _ := s.Get(TLSKey)
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("certificado e chave nao formam um par: %v", err)
	}
	leaf, _ := x509.ParseCertificate(pair.Certificate[0])
	_, err = leaf.Verify(x509.VerifyOptions{DNSName: name, Roots: pool, CurrentTime: at})
	return err
}

func TestLocalTLSValeParaOsHosts(t *testing.T) {
	s := newStore(t)
	changed, err := EnsureLocalTLS(s, hosts, domains, t0)
	if err != nil || !changed {
		t.Fatalf("EnsureLocalTLS = %v, %v", changed, err)
	}
	for _, name := range []string{"registry.trd.localhost", "db.trd.localhost", "ci.trd.localhost", "trd.localhost"} {
		if err := verify(t, s, name, t0); err != nil {
			t.Errorf("%s nao valida: %v", name, err)
		}
	}
	if err := verify(t, s, "google.com", t0); err == nil {
		t.Error("certificado valeu pra google.com")
	}
}

// a CA instalada como confiavel nao pode servir pra forjar outro dominio
func TestCANaoAssinaForaDoDominio(t *testing.T) {
	s := newStore(t)
	if _, err := EnsureLocalTLS(s, []string{"banco.com.br"}, domains, t0); err != nil {
		t.Fatal(err)
	}
	err := verify(t, s, "banco.com.br", t0)
	if err == nil {
		t.Fatal("CA restrita a trd.localhost validou banco.com.br")
	}
	if _, ok := err.(x509.CertificateInvalidError); !ok {
		t.Fatalf("esperava erro de name constraint, veio %T: %v", err, err)
	}
}

func TestLocalTLSIdempotenteERenova(t *testing.T) {
	s := newStore(t)
	EnsureLocalTLS(s, hosts, domains, t0)
	ca1, _ := s.Get(TLSCACert)
	leaf1, _ := s.Get(TLSCert)

	if changed, _ := EnsureLocalTLS(s, hosts, domains, t0.Add(24*time.Hour)); changed {
		t.Error("refez o certificado sem motivo")
	}

	// host novo: certificado novo, mesma CA (quem confiou nao refaz nada)
	more := append(hosts, "ci.outro.trd.localhost")
	if changed, _ := EnsureLocalTLS(s, more, domains, t0); !changed {
		t.Error("nao refez o certificado com host novo")
	}
	if ca2, _ := s.Get(TLSCACert); !bytes.Equal(ca1, ca2) {
		t.Error("CA mudou so por causa de um host novo")
	}
	if err := verify(t, s, "ci.outro.trd.localhost", t0); err != nil {
		t.Error(err)
	}

	// perto de vencer: renova
	leaf2, _ := s.Get(TLSCert)
	late := t0.Add(leafValidity - 10*24*time.Hour)
	if changed, _ := EnsureLocalTLS(s, more, domains, late); !changed {
		t.Error("nao renovou certificado perto de vencer")
	}
	if leaf3, _ := s.Get(TLSCert); bytes.Equal(leaf2, leaf3) || bytes.Equal(leaf1, leaf3) {
		t.Error("certificado nao mudou")
	}
	if err := verify(t, s, "registry.trd.localhost", late); err != nil {
		t.Error(err)
	}
}

func TestDominioNovoRefazCA(t *testing.T) {
	s := newStore(t)
	EnsureLocalTLS(s, hosts, domains, t0)
	ca1, _ := s.Get(TLSCACert)

	EnsureLocalTLS(s, []string{"teste.localhost", "*.teste.localhost"}, []string{"teste.localhost"}, t0)
	if ca2, _ := s.Get(TLSCACert); bytes.Equal(ca1, ca2) {
		t.Fatal("CA antiga continuou com o dominio novo")
	}
	if err := verify(t, s, "x.teste.localhost", t0); err != nil {
		t.Error(err)
	}
	if err := verify(t, s, "registry.trd.localhost", t0); err == nil {
		t.Error("certificado novo ainda vale pro dominio antigo")
	}
}
