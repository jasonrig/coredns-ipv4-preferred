package integration_test

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
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jasonrig/coredns-ipv4-preferred/internal/fixtures"
	"github.com/miekg/dns"
)

func tlsUpstream(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "resolver.test"}, DNSNames: []string{"resolver.test"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pair, err := tls.X509KeyPair(certPEM, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: encoded}))
	if err != nil {
		t.Fatal(err)
	}
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{pair}})
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{Listener: listener, Net: "tcp-tls", Handler: fixtures.New()}
	go func() { _ = server.ActivateAndServe() }()
	t.Cleanup(func() { _ = server.Shutdown() })
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, certPEM, 0600); err != nil {
		t.Fatal(err)
	}
	return "tls://" + listener.Addr().String(), ca
}

// This loads the shipped Corefile (including its environment substitutions),
// changing only bind ports, the upstream addresses and the fixture CA trust root.
// A wrong TLS name must fail closed; it must never silently use plaintext DNS.
func TestSuppliedCorefileWithVerifiedTLS(t *testing.T) {
	for _, validName := range []bool{true, false} {
		t.Run(fmt.Sprint(validName), func(t *testing.T) {
			endpoint, ca := tlsUpstream(t)
			original, err := os.ReadFile("../Corefile")
			if err != nil {
				t.Fatal(err)
			}
			dnsPort, readyPort, healthPort, metricsPort := freePort(t), freePort(t), freePort(t), freePort(t)
			config := strings.NewReplacer(".:1053 {", fmt.Sprintf(".:%d {\n bind 127.0.0.1", dnsPort), "health :8080", fmt.Sprintf("health 127.0.0.1:%d", healthPort), "ready :8181", fmt.Sprintf("ready 127.0.0.1:%d", readyPort), "prometheus :9153", fmt.Sprintf("prometheus 127.0.0.1:%d", metricsPort), "tls_servername", "tls "+ca+"\n        tls_servername").Replace(string(original))
			name := "resolver.test"
			if !validName {
				name = "wrong.test"
			}
			addr, _ := startConfig(t, config, dnsPort, readyPort, metricsPort, []string{"UPSTREAM_1=" + endpoint, "UPSTREAM_2=" + endpoint, "TLS_SERVER_NAME=" + name})
			m := ask(t, addr, "udp", "dual.test.", dns.TypeA)
			if validName {
				if m.Rcode != dns.RcodeSuccess || count(m, dns.TypeA) != 3 {
					t.Fatal("verified TLS failed", m)
				}
				if count(ask(t, addr, "tcp", "dual.test.", dns.TypeAAAA), dns.TypeAAAA) != 0 {
					t.Fatal("policy failed over TLS")
				}
			} else if m.Rcode != dns.RcodeServerFailure {
				t.Fatal("wrong certificate name was accepted", m)
			}
		})
	}
}
