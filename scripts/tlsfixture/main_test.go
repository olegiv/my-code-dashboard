// SPDX-License-Identifier: GPL-3.0-only
package main

import (
	"crypto/x509"
	"testing"
	"time"
)

func TestCertificatesChainToSyntheticCAOnly(t *testing.T) {
	now := time.Now()
	caPEM, certificate, err := certificates("gitlab.ca-check.invalid", now)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		t.Fatal("CA output is not a PEM certificate")
	}
	options := x509.VerifyOptions{Roots: roots, DNSName: "gitlab.ca-check.invalid", CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if _, err = leaf.Verify(options); err != nil {
		t.Fatalf("server certificate does not chain to the printed CA: %v", err)
	}
	options.DNSName = "gitlab.example.com"
	if _, err = leaf.Verify(options); err == nil {
		t.Fatal("server certificate verified for another host")
	}
	options.DNSName, options.Roots = "gitlab.ca-check.invalid", x509.NewCertPool()
	if _, err = leaf.Verify(options); err == nil {
		t.Fatal("server certificate verified without the synthetic CA")
	}
}
