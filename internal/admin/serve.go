package admin

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/acme/autocert"
)

// Run serves the panel until ctx is cancelled:
//   - LocalhostOnly: plain HTTP on 127.0.0.1, the only mode without TLS;
//   - Hostname: a Let's Encrypt certificate over TLS-ALPN-01 (needs port 443), with HSTS;
//   - otherwise: a self-signed certificate, made once and kept, its fingerprint logged.
func (a *App) Run(ctx context.Context) error {
	cfg := a.cfg
	if cfg.Listen == "" {
		cfg.Listen = ":443"
	}
	_, port, err := net.SplitHostPort(cfg.Listen)
	if err != nil {
		return fmt.Errorf("listen address %q: %w", cfg.Listen, err)
	}
	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sctx)
	}()

	switch {
	case cfg.LocalhostOnly && cfg.Hostname != "":
		return errors.New("a panel hostname needs HTTPS; it can't be used with localhost-only mode")
	case cfg.LocalhostOnly:
		srv.Addr = net.JoinHostPort("127.0.0.1", port)
		log.Printf("admin: serving http://%s/ (localhost only: reach it through an SSH tunnel)", srv.Addr)
		err = srv.ListenAndServe()
	case cfg.Hostname != "":
		if port != "443" {
			log.Printf("admin: warning: Let's Encrypt checks %s on port 443, but the panel listens on %s", cfg.Hostname, cfg.Listen)
		}
		m := &autocert.Manager{
			Prompt:     autocert.AcceptTOS,
			HostPolicy: autocert.HostWhitelist(cfg.Hostname),
			Cache:      autocert.DirCache(filepath.Join(cfg.AdminDir, "acme")),
		}
		srv.TLSConfig = m.TLSConfig()
		log.Printf("admin: serving https://%s/ with a Let's Encrypt certificate", cfg.Hostname)
		err = srv.ListenAndServeTLS("", "")
	default:
		cert, fp, cerr := selfSignedCert(filepath.Join(cfg.AdminDir, "tls"), a.clock.Now())
		if cerr != nil {
			return cerr
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		log.Printf("admin: serving HTTPS on %s with a self-signed certificate, SHA-256 fingerprint %s", cfg.Listen, fp)
		err = srv.ListenAndServeTLS("", "")
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// selfSignedCert loads dir/cert.pem and dir/key.pem, or makes them on first
// run, and returns the certificate with its SHA-256 fingerprint.
func selfSignedCert(dir string, now time.Time) (tls.Certificate, string, error) {
	certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, fingerprint(cert.Certificate[0]), nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return tls.Certificate{}, "", err
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return tls.Certificate{}, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "thefeed-admin"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return tls.Certificate{}, "", err
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		return tls.Certificate{}, "", err
	}
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	return cert, fingerprint(der), err
}

// fingerprint is the SHA-256 of a certificate as browsers show it: AB:CD:...
func fingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	parts := make([]string, len(sum))
	for i, b := range sum {
		parts[i] = fmt.Sprintf("%02X", b)
	}
	return strings.Join(parts, ":")
}
