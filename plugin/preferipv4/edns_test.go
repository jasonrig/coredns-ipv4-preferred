package preferipv4

import (
	"context"
	"github.com/miekg/dns"
	"testing"
)

func TestEDNSCookieAndECS(t *testing.T) {
	for _, ecs := range []bool{false, true} {
		f := withAnswers([]string{"host.test. 30 IN AAAA 2001:db8::1"}, []string{"host.test. 30 IN A 192.0.2.1"})
		base := f.Next
		probes := 0
		f.Next = handler(func(ctx context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
			if q.Question[0].Qtype == dns.TypeA {
				probes++
			}
			return base.ServeDNS(ctx, w, q)
		})
		q := query("host.test.", dns.TypeAAAA)
		q.SetEdns0(1232, false)
		if ecs {
			q.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_SUBNET{Code: dns.EDNS0SUBNET, Family: 1, SourceNetmask: 0}}
		} else {
			q.IsEdns0().Option = []dns.EDNS0{&dns.EDNS0_COOKIE{Code: dns.EDNS0COOKIE, Cookie: "aabbccddeeff0011"}}
		}
		got := run(t, f, q)
		if ecs {
			if countType(got, dns.TypeAAAA) != 1 || probes != 0 {
				t.Fatal("ECS was not bypassed")
			}
		} else {
			if countType(got, dns.TypeAAAA) != 0 || probes != 1 {
				t.Fatal("ordinary COOKIE query bypassed policy")
			}
		}
	}
}

func TestCNAMENoDataDoesNotRepeatLookup(t *testing.T) {
	f := withAnswers([]string{"host.test. 40 IN CNAME v6.test.", "v6.test. 30 IN AAAA 2001:db8::1"}, nil)
	base := f.Next
	probes := 0
	f.Next = handler(func(ctx context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
		if q.Question[0].Qtype != dns.TypeA {
			return base.ServeDNS(ctx, w, q)
		}
		probes++
		m := answer(q, "host.test. 40 IN CNAME v6.test.")
		m.Ns = []dns.RR{rr("test. 30 IN SOA ns.test. hostmaster.test. 1 60 60 60 30")}
		return 0, w.WriteMsg(m)
	})
	if countType(run(t, f, query("host.test.", dns.TypeAAAA)), dns.TypeAAAA) != 1 || probes != 1 {
		t.Fatalf("NODATA required %d A probes", probes)
	}
}
