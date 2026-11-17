// Package serverconf is the web server's own settings, the [web] section of
// gohort.ini, as the admin page reads, checks and saves them: the address it
// listens on, HTTPS, how many apps run at once, who may reach the admin
// pages, and which reverse proxies are believed.
//
// The settings live in the ini file, which only the main package holds, so
// main supplies Load and Save; the admin page goes through them and never
// the file. Running is what the process started with, so the page can say
// which saved changes are waiting for a restart: the address, HTTPS and the
// app limit are read once at start, while the two address lists are read on
// every request and apply at once.
package serverconf

import (
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// Settings are the web server's settings.
type Settings struct {
	Addr            string `json:"addr"`
	TLSCert         string `json:"tls_cert"`
	TLSKey          string `json:"tls_key"`
	TLSSelfSigned   bool   `json:"tls_self_signed"`
	MaxConcurrent   int    `json:"max_concurrent"`
	AdminAllowedIPs string `json:"admin_allowed_ips"`
	TrustedProxies  string `json:"trusted_proxies"`
}

var (
	// Load reads the saved settings; Save writes them. Set by main. Nil
	// means this build cannot change them from the admin page.
	Load func() Settings
	Save func(Settings) error
	// Running is what this process started with, set by main once it
	// knows; AddrGiven is true when the address was given on the command
	// line, which a saved address does not override.
	Running   Settings
	AddrGiven bool
)

// Normalize trims every field, and reads an app limit below one as one.
func (s Settings) Normalize() Settings {
	s.Addr = strings.TrimSpace(s.Addr)
	s.TLSCert = strings.TrimSpace(s.TLSCert)
	s.TLSKey = strings.TrimSpace(s.TLSKey)
	s.AdminAllowedIPs = strings.TrimSpace(s.AdminAllowedIPs)
	s.TrustedProxies = strings.TrimSpace(s.TrustedProxies)
	if s.MaxConcurrent < 1 {
		s.MaxConcurrent = 1
	}
	return s
}

// maxConcurrentCap bounds the app limit: a number past it is a typo, and
// every app it lets run at once holds a model call and memory.
const maxConcurrentCap = 64

// Check says what is wrong with s, nil when nothing is. HTTPS files are
// opened and parsed: a certificate that does not load would stop the
// server coming back after its restart, with nobody able to reach this page
// to fix it.
func Check(s Settings) error {
	s = s.Normalize()
	if s.Addr != "" {
		host, port, err := net.SplitHostPort(s.Addr)
		if err != nil {
			return fmt.Errorf("the address %q is not host:port (for every interface, write :8080)", s.Addr)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return fmt.Errorf("the port %q is not a port number (1 to 65535)", port)
		}
		if host != "" && net.ParseIP(host) == nil && strings.ContainsAny(host, " /") {
			return fmt.Errorf("the host %q is not an address or a name", host)
		}
	}
	if (s.TLSCert == "") != (s.TLSKey == "") {
		return fmt.Errorf("HTTPS needs both the certificate and its key, or neither")
	}
	if s.TLSCert != "" {
		if s.TLSSelfSigned {
			return fmt.Errorf("choose a certificate or a self-signed one, not both")
		}
		if _, err := tls.LoadX509KeyPair(s.TLSCert, s.TLSKey); err != nil {
			return fmt.Errorf("the certificate and key do not load: %v", err)
		}
	}
	if s.MaxConcurrent > maxConcurrentCap {
		return fmt.Errorf("at most %d apps at once", maxConcurrentCap)
	}
	if err := CheckAddressList(s.AdminAllowedIPs); err != nil {
		return fmt.Errorf("admin addresses: %v", err)
	}
	if err := CheckAddressList(s.TrustedProxies); err != nil {
		return fmt.Errorf("trusted proxies: %v", err)
	}
	return nil
}

// CheckAddressList says which entry of a comma-separated list of addresses
// or CIDR ranges is neither, nil when all are. An entry that does not parse
// is dropped by the reader, so a typo in the admin list silently admits
// less, or in the proxy list believes nobody: refused here instead.
func CheckAddressList(list string) error {
	for _, e := range strings.Split(list, ",") {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if strings.Contains(e, "/") {
			if _, _, err := net.ParseCIDR(e); err != nil {
				return fmt.Errorf("%q is not a range (write 192.168.1.0/24)", e)
			}
			continue
		}
		if net.ParseIP(e) == nil {
			return fmt.Errorf("%q is not an address", e)
		}
	}
	return nil
}

// WaitingForRestart names the saved settings that differ from what the
// process is running with and apply only when it restarts. The address is
// left out when the command line gave one: a saved address does not apply.
func WaitingForRestart(saved Settings) []string {
	saved, run := saved.Normalize(), Running.Normalize()
	var out []string
	if !AddrGiven && saved.Addr != "" && saved.Addr != run.Addr {
		out = append(out, "the address")
	}
	if saved.TLSCert != run.TLSCert || saved.TLSKey != run.TLSKey || saved.TLSSelfSigned != run.TLSSelfSigned {
		out = append(out, "HTTPS")
	}
	if saved.MaxConcurrent != run.MaxConcurrent {
		out = append(out, "the app limit")
	}
	return out
}
