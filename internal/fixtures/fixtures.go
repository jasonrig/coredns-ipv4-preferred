// Package fixtures provides controlled DNS records for tests, never production.
package fixtures

import (
	"net"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/miekg/dns"
)

// Resolver is a thread-safe deterministic mock recursive resolver.
type Resolver struct {
	NoIPv4 atomic.Bool
	mu     sync.Mutex
	counts map[string]int
}

// New creates a resolver whose records are all beneath the reserved .test TLD.
func New() *Resolver { return &Resolver{counts: make(map[string]int)} }

// Count returns the number of upstream lookups observed for a name and type.
func (r *Resolver) Count(name string, typ uint16) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.counts[name+dns.TypeToString[typ]]
}
func record(s string) dns.RR {
	rr, err := dns.NewRR(s)
	if err != nil {
		panic(err)
	}
	return rr
}

// ServeDNS answers controlled A, AAAA, CNAME and unrelated record types.
func (r *Resolver) ServeDNS(w dns.ResponseWriter, q *dns.Msg) {
	m := new(dns.Msg)
	m.SetReply(q)
	m.RecursionAvailable = true
	if len(q.Question) != 1 {
		m.Rcode = dns.RcodeFormatError
		_ = w.WriteMsg(m)
		return
	}
	name, typ := strings.ToLower(q.Question[0].Name), q.Question[0].Qtype
	r.mu.Lock()
	r.counts[name+dns.TypeToString[typ]]++
	r.mu.Unlock()
	if name == "missing.test." {
		m.Rcode = dns.RcodeNameError
	}
	if name == "failure.test." || (name == "servfail.test." && typ == dns.TypeA) {
		m.Rcode = dns.RcodeServerFailure
	}
	if name == "timeout.test." && typ == dns.TypeA {
		return // Deliberately blackhole this lookup; never populate the A cache.
	}
	if typ == dns.TypeAAAA || typ == dns.TypeA {
		terminal := name
		switch name {
		case "alias.test.", "alias6.test.":
			terminal = "dual.test."
			if name == "alias6.test." {
				terminal = "v6.test."
			}
			m.Answer = append(m.Answer, record(name+" 45 IN CNAME "+terminal))
		case "multi.test.", "split.test.":
			m.Answer = append(m.Answer, record(name+" 45 IN CNAME middle.test."))
			if name == "split.test." && typ == dns.TypeA {
				_ = w.WriteMsg(m)
				return
			}
			m.Answer = append(m.Answer, record("middle.test. 40 IN CNAME dual.test."))
			terminal = "dual.test."
		case "middle.test.":
			m.Answer = append(m.Answer, record("middle.test. 40 IN CNAME dual.test."))
			terminal = "dual.test."
		}
		ttl := "60"
		if terminal == "cache.test." {
			ttl = "3"
		}
		if terminal == "change.test." && typ == dns.TypeA {
			ttl = "1"
		}
		hasA := terminal != "v6.test." && terminal != "nodata.test." && terminal != "missing.test." && terminal != "failure.test."
		has6 := terminal != "v4.test." && terminal != "nodata.test." && terminal != "missing.test." && terminal != "failure.test."
		if terminal == "change.test." && r.NoIPv4.Load() {
			hasA = false
		}
		if typ == dns.TypeA && hasA && m.Rcode == dns.RcodeSuccess {
			if name == "large.test." {
				if _, udp := w.LocalAddr().(*net.UDPAddr); udp {
					m.Truncated = true
					m.Answer = nil
					_ = w.WriteMsg(m)
					return
				}
			}
			for _, ip := range []string{"192.0.2.10", "192.0.2.11", "192.0.2.12"} {
				m.Answer = append(m.Answer, record(terminal+" "+ttl+" IN A "+ip))
			}
		}
		if typ == dns.TypeAAAA && has6 && m.Rcode == dns.RcodeSuccess {
			for _, ip := range []string{"2001:db8::10", "2001:db8::11"} {
				m.Answer = append(m.Answer, record(terminal+" "+ttl+" IN AAAA "+ip))
			}
		}
	} else {
		switch typ {
		case dns.TypeTXT:
			m.Answer = []dns.RR{record(name + " 60 IN TXT \"fixture\"")}
		case dns.TypeMX:
			m.Answer = []dns.RR{record(name + " 60 IN MX 10 mail.test.")}
		case dns.TypeSRV:
			m.Answer = []dns.RR{record(name + " 60 IN SRV 1 2 443 service.test.")}
		case dns.TypePTR:
			m.Answer = []dns.RR{record(name + " 60 IN PTR target.test.")}
		case dns.TypeHTTPS:
			m.Answer = []dns.RR{record(name + " 60 IN HTTPS 1 . ipv6hint=2001:db8::10")}
		case dns.TypeSVCB:
			m.Answer = []dns.RR{record(name + " 60 IN SVCB 1 . ipv6hint=2001:db8::11")}
		}
	}
	if len(m.Answer) == 0 || (typ == dns.TypeA && strings.HasPrefix(name, "alias6.")) {
		m.Ns = []dns.RR{record("test. 30 IN SOA ns.test. hostmaster.test. 1 60 60 60 30")}
	}
	_ = w.WriteMsg(m)
}
