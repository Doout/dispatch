package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"time"
)

func consoleTLSConfig(config configuration, managed func(*tls.ClientHelloInfo) (*tls.Certificate, error)) (*tls.Config, error) {
	result := &tls.Config{MinVersion: tls.VersionTLS12}
	if config.TLSMode != "acme" {
		return result, nil
	}
	var fallback *tls.Certificate
	if config.CertificateFile != "" {
		certificate, err := tls.LoadX509KeyPair(config.CertificateFile, config.KeyFile)
		if err != nil {
			return nil, errors.New("cannot load bootstrap TLS certificate")
		}
		leaf, err := x509.ParseCertificate(certificate.Certificate[0])
		if err != nil || leaf.VerifyHostname(config.Hosted.RootDomain) != nil {
			return nil, errors.New("bootstrap TLS certificate must cover the root console hostname")
		}
		now := time.Now()
		if now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
			return nil, errors.New("bootstrap TLS certificate is outside its validity period")
		}
		certificate.Leaf = leaf
		fallback = &certificate
	}
	result.GetCertificate = func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
		certificate, err := managed(hello)
		if err == nil || fallback == nil || !strings.EqualFold(hello.ServerName, config.Hosted.RootDomain) {
			return certificate, err
		}
		// Only the root console can use the supplied certificate while managed
		// issuance is unavailable. Do not keep serving it after it expires.
		now := time.Now()
		if now.Before(fallback.Leaf.NotBefore) || !now.Before(fallback.Leaf.NotAfter) {
			return nil, errors.New("bootstrap TLS certificate is outside its validity period")
		}
		return fallback, nil
	}
	return result, nil
}
