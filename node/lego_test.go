package node

import (
	"log"
	"os"
	"path/filepath"
	"testing"

	"github.com/InazumaV/V2bX/conf"
)

func newTestLego(t *testing.T) *Lego {
	t.Helper()

	// This test requires external network + DNS provider credentials.
	// Run explicitly:
	//   RUN_LEGO_INTEGRATION=1 CF_DNS_API_TOKEN=... LEGO_CERT_DOMAIN=... LEGO_EMAIL=... go test ./node -run TestLego
	if os.Getenv("RUN_LEGO_INTEGRATION") != "1" {
		t.Skip("skipping lego integration test (set RUN_LEGO_INTEGRATION=1 to run)")
	}

	token := os.Getenv("CF_DNS_API_TOKEN")
	domain := os.Getenv("LEGO_CERT_DOMAIN")
	email := os.Getenv("LEGO_EMAIL")
	if token == "" || domain == "" || email == "" {
		t.Skip("missing env: CF_DNS_API_TOKEN, LEGO_CERT_DOMAIN, LEGO_EMAIL")
	}

	dir := t.TempDir()
	l, err := NewLego(&conf.CertConfig{
		CertMode:   "dns",
		Email:      email,
		CertDomain: domain,
		Provider:   "cloudflare",
		DNSEnv: map[string]string{
			"CF_DNS_API_TOKEN": token,
		},
		CertFile: filepath.Join(dir, "cert.pem"),
		KeyFile:  filepath.Join(dir, "cert.key"),
	})
	if err != nil {
		t.Fatal(err)
	}
	return l
}

func TestLego_CreateCertByDns(t *testing.T) {
	l := newTestLego(t)
	err := l.CreateCert()
	if err != nil {
		t.Error(err)
	}
}

func TestLego_RenewCert(t *testing.T) {
	l := newTestLego(t)
	log.Println(l.RenewCert())
}
