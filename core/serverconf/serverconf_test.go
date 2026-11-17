package serverconf

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writePair writes a self-made certificate and its key, the way an
// administrator's PEM files would sit on disk.
func writePair(t *testing.T) (cert, key string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "gohort.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := x509.MarshalECPrivateKey(priv)
	dir := t.TempDir()
	cert, key = filepath.Join(dir, "c.pem"), filepath.Join(dir, "k.pem")
	os.WriteFile(cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600)
	os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kb}), 0o600)
	return cert, key
}

// What would stop the server coming back, or silently admit less than meant,
// is refused before it is saved; the rest passes.
func TestSettingsThatWouldBreakTheServerAreRefused(t *testing.T) {
	cert, key := writePair(t)
	ok := []Settings{
		{},
		{Addr: ":8080"},
		{Addr: "127.0.0.1:8443", TLSCert: cert, TLSKey: key, MaxConcurrent: 4},
		{TLSSelfSigned: true, AdminAllowedIPs: "192.168.1.0/24, 10.0.0.5", TrustedProxies: "::1, 192.168.0.29"},
	}
	for _, s := range ok {
		if err := Check(s); err != nil {
			t.Errorf("%+v refused: %v", s, err)
		}
	}
	bad := map[string]Settings{
		"host:port":            {Addr: "8080"},
		"port number":          {Addr: ":99999"},
		"both the certificate": {TLSCert: cert},
		"do not load":          {TLSCert: cert, TLSKey: cert},
		"not both":             {TLSCert: cert, TLSKey: key, TLSSelfSigned: true},
		"at most":              {MaxConcurrent: 500},
		"admin addresses":      {AdminAllowedIPs: "192.168.1.0/24, 192.168.1"},
		"trusted proxies":      {TrustedProxies: "proxy.local"},
	}
	for want, s := range bad {
		if err := Check(s); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%+v: %v, want it to say %q", s, err, want)
		}
	}
}

// The page names the saved settings a restart would bring in; a saved
// address does not count when the command line gave one.
func TestTheSettingsWaitingForARestartAreNamed(t *testing.T) {
	Running, AddrGiven = Settings{Addr: ":8080", MaxConcurrent: 1}, false
	t.Cleanup(func() { Running, AddrGiven = Settings{}, false })
	if w := WaitingForRestart(Settings{Addr: ":8080", AdminAllowedIPs: "10.0.0.0/8"}); len(w) != 0 {
		t.Errorf("nothing that waits for a restart changed, yet: %v", w)
	}
	w := WaitingForRestart(Settings{Addr: ":9090", TLSSelfSigned: true, MaxConcurrent: 3})
	if strings.Join(w, ", ") != "the address, HTTPS, the app limit" {
		t.Errorf("waiting: %v", w)
	}
	AddrGiven = true
	if w := WaitingForRestart(Settings{Addr: ":9090", MaxConcurrent: 1}); len(w) != 0 {
		t.Errorf("a saved address counted though the command line names one: %v", w)
	}
}
