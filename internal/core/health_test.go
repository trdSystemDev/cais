package core

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"path/filepath"
	"testing"
	"time"

	"github.com/trdSystemDev/cais/internal/config"
)

func certPEM(t *testing.T, domain string, notAfter time.Time) string {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: domain},
		NotBefore: notAfter.Add(-90 * 24 * time.Hour), NotAfter: notAfter, DNSNames: []string{domain}}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestCheckHealth(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	soon, later := now.Add(5*24*time.Hour).Truncate(time.Second), now.Add(80*24*time.Hour).Truncate(time.Second)
	acme := fmt.Sprintf(`{"le":{"Certificates":[{"domain":{"main":"b.exemplo.com"},"certificate":%q},`+
		`{"domain":{"main":"a.exemplo.com"},"certificate":%q}]}}`, certPEM(t, "b.exemplo.com", later), certPEM(t, "a.exemplo.com", soon))
	releases, reads := 0, 0
	var execErr error
	c := New(Deps{
		ConfigPath: filepath.Join(root, "config.yaml"),
		Now:        func() time.Time { return now },
		Version:    "v0.1.0",
		LatestRelease: func(context.Context) (string, error) {
			releases++
			return "v0.2.0", nil
		},
		Exec: func(_ context.Context, container string, args ...string) (string, error) {
			reads++
			if container != "proxy-traefik" {
				t.Errorf("exec em %s", container)
			}
			return acme, execErr
		},
	})
	ctx := context.Background()

	// sem config (proxy desligado): nada de certificado pra ler
	h, _ := c.CheckHealth(ctx)
	if reads != 0 || len(h.Certs) != 0 || !h.UpdateAvailable() || h.Latest != "v0.2.0" {
		t.Fatalf("sem proxy: %+v (leituras %d)", h, reads)
	}

	cfg := config.Default()
	cfg.Email = "a@exemplo.com"
	cfg.Stacks = append(cfg.Stacks, "proxy")
	if err := c.SaveConfig(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	now = now.Add(certsEvery)
	h, _ = c.CheckHealth(ctx)
	if len(h.Certs) != 2 || h.Certs[0].Domain != "a.exemplo.com" || !h.Certs[0].NotAfter.Equal(soon) {
		t.Fatalf("certificados: %+v", h.Certs)
	}
	if releases != 1 {
		t.Fatalf("consultou o release %d vezes antes do intervalo", releases)
	}
	if st, _ := c.Snapshot(ctx); len(st.Health.Certs) != 2 {
		t.Fatal("nao publicou no estado")
	}

	// antes do intervalo nao le de novo
	c.CheckHealth(ctx)
	if reads != 1 {
		t.Fatalf("leu os certificados %d vezes", reads)
	}
	// erro lendo vira texto
	now, execErr = now.Add(certsEvery), errors.New("container parado")
	if h, err := c.CheckHealth(ctx); err != nil || h.CertsError == "" || h.Latest != "v0.2.0" {
		t.Fatalf("erro: %+v %v", h, err)
	}
}

func TestVersaoNova(t *testing.T) {
	for _, tc := range []struct {
		latest, cur string
		want        bool
	}{
		{"v0.2.0", "v0.1.9", true}, {"v1.0.0", "v0.9.0", true}, {"v0.1.10", "v0.1.9", true},
		{"v0.1.0", "v0.1.0", false}, {"v0.1.0", "v0.2.0", false}, {"v0.2.0", "dev", false}, {"", "v0.1.0", false},
	} {
		if got := (Health{Latest: tc.latest, Version: tc.cur}).UpdateAvailable(); got != tc.want {
			t.Errorf("%s vs %s: %v", tc.latest, tc.cur, got)
		}
	}
}

func TestACMEVazio(t *testing.T) {
	if certs, err := parseACME([]byte("")); err != nil || certs != nil {
		t.Fatalf("%v %v", certs, err)
	}
	if _, err := parseACME([]byte("{nao json")); err == nil {
		t.Fatal("aceitou acme.json quebrado")
	}
}
