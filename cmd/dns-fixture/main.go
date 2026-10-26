// Test-only upstream used by scripts/smoke-container.sh; excluded from runtime image.
package main

import (
	"github.com/jasonrig/coredns-ipv4-preferred/internal/fixtures"
	"github.com/miekg/dns"
	"log"
)

func main() {
	r := fixtures.New()
	go func() { log.Fatal((&dns.Server{Addr: ":5353", Net: "tcp", Handler: r}).ListenAndServe()) }()
	log.Fatal((&dns.Server{Addr: ":5353", Net: "udp", Handler: r}).ListenAndServe())
}
