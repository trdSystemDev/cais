package backup

import (
	"strings"
	"testing"
)

// sem o client no build (go build sem os -X do release), o login do Google
// diz o que falta em vez de ir pro Google com client vazio
func TestGoogleClientDoBuild(t *testing.T) {
	id, secret := GoogleClientID, GoogleClientSecret
	t.Cleanup(func() { GoogleClientID, GoogleClientSecret = id, secret })

	GoogleClientID, GoogleClientSecret = "", ""
	if _, err := GoogleClient(); err == nil || !strings.Contains(err.Error(), "CAIS_GOOGLE_CLIENT_ID") {
		t.Fatalf("sem client: %v", err)
	}
	GoogleClientID, GoogleClientSecret = "x.apps.googleusercontent.com", "s"
	if c, err := GoogleClient(); err != nil || c.ID != "x.apps.googleusercontent.com" || c.Secret != "s" {
		t.Fatalf("client %+v %v", c, err)
	}
}
