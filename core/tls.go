package core

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// TLS configuration for the web server.
var (
	// TLSCert is the path to a PEM-encoded TLS certificate file.
	TLSCert string

	// TLSKey is the path to a PEM-encoded TLS private key file.
	TLSKey string

	// TLSSelfSigned enables automatic self-signed certificate generation
	// when TLSCert and TLSKey are empty. The generated cert/key are
	// written to the application data directory and reused on restart.
	TLSSelfSigned bool
)

// TLSEnabled reports whether TLS is configured (either explicit certs or self-signed).
func TLSEnabled() bool {
	return (TLSCert != "" && TLSKey != "") || TLSSelfSigned
}

// ListenAndServeTLS starts an HTTPS server using the configured TLS settings.
// If TLSCert/TLSKey are set, those files are used directly. If TLSSelfSigned
// is true and no explicit certs are provided, a self-signed certificate is
// generated (and cached to disk for reuse). Falls back to plain HTTP if TLS
// is not configured.
func ListenAndServeTLS(addr string, handler http.Handler) error {
	if !TLSEnabled() {
		return http.ListenAndServe(addr, handler)
	}

	cert_file, key_file, err := resolveTLSFiles()
	if err != nil {
		return fmt.Errorf("TLS setup failed: %w", err)
	}

	tlsCert, err := tls.LoadX509KeyPair(cert_file, key_file)
	if err != nil {
		return fmt.Errorf("failed to load TLS certificate: %w", err)
	}

	server := &http.Server{
		Addr:    addr,
		Handler: handler,
		TLSConfig: &tls.Config{
			Certificates: []tls.Certificate{tlsCert},
			MinVersion:   tls.VersionTLS12,
		},
	}

	// TLS cert/key already loaded into config, pass empty strings.
	return server.ListenAndServeTLS("", "")
}

// resolveTLSFiles returns the cert and key file paths, generating a
// self-signed pair if needed.
func resolveTLSFiles() (cert_file, key_file string, err error) {
	if TLSCert != "" && TLSKey != "" {
		return TLSCert, TLSKey, nil
	}

	// Self-signed mode: generate or reuse cached files.
	exec, err := os.Executable()
	if err != nil {
		return "", "", fmt.Errorf("cannot locate executable: %w", err)
	}
	data_dir := GetPath(filepath.Join(filepath.Dir(exec), "data", "tls"))
	if err := os.MkdirAll(data_dir, 0700); err != nil {
		return "", "", fmt.Errorf("cannot create TLS directory %s: %w", data_dir, err)
	}

	cert_file = filepath.Join(data_dir, "self_signed.crt")
	key_file = filepath.Join(data_dir, "self_signed.key")

	// Reuse existing files if they exist and haven't expired.
	if fileExists(cert_file) && fileExists(key_file) {
		if !certExpired(cert_file) {
			Log("Using cached self-signed certificate from %s", data_dir)
			return cert_file, key_file, nil
		}
		Log("Self-signed certificate expired, regenerating.")
	}

	Log("Generating self-signed TLS certificate...")
	return cert_file, key_file, generateSelfSigned(cert_file, key_file)
}

// generateSelfSigned creates a self-signed certificate valid for 1 year,
// covering localhost and common LAN addresses.
func generateSelfSigned(cert_file, key_file string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("generate key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("generate serial: %w", err)
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{Organization: []string{"Gohort"}, CommonName: "localhost"},
		NotBefore:    now,
		NotAfter:     now.Add(365 * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}

	// Add LAN IPs so the cert works across the local network.
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				template.IPAddresses = append(template.IPAddresses, ipnet.IP)
			}
		}
	}

	cert_der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return fmt.Errorf("create certificate: %w", err)
	}

	// Write cert file.
	cf, err := os.OpenFile(cert_file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	if err := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: cert_der}); err != nil {
		cf.Close()
		return err
	}
	cf.Close()

	// Write key file (restricted permissions).
	key_der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	kf, err := os.OpenFile(key_file, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if err := pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: key_der}); err != nil {
		kf.Close()
		return err
	}
	kf.Close()

	Log("Self-signed certificate generated: %s", cert_file)
	return nil
}

// fileExists reports whether a file exists.
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// certExpired checks whether a PEM certificate file has expired or will
// expire within 7 days.
func certExpired(cert_file string) bool {
	data, err := os.ReadFile(cert_file)
	if err != nil {
		return true
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return true
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return true
	}
	return time.Now().Add(7 * 24 * time.Hour).After(cert.NotAfter)
}
