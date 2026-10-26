package preferipv4

import (
	"context"
	"errors"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/plugin"
	"github.com/miekg/dns"
)

type handler func(context.Context, dns.ResponseWriter, *dns.Msg) (int, error)

func (h handler) ServeDNS(c context.Context, w dns.ResponseWriter, r *dns.Msg) (int, error) {
	return h(c, w, r)
}
func (handler) Name() string { return "fixture" }

func rr(s string) dns.RR {
	r, e := dns.NewRR(s)
	if e != nil {
		panic(e)
	}
	return r
}
func query(name string, typ uint16) *dns.Msg { m := new(dns.Msg); m.SetQuestion(name, typ); return m }
func answer(q *dns.Msg, records ...string) *dns.Msg {
	m := new(dns.Msg)
	m.SetReply(q)
	m.RecursionAvailable = true
	for _, s := range records {
		m.Answer = append(m.Answer, rr(s))
	}
	return m
}
func recorder() *capture {
	return newCapture(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1053}, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 50000})
}
func countType(m *dns.Msg, typ uint16) int {
	n := 0
	for _, r := range m.Answer {
		if r.Header().Rrtype == typ {
			n++
		}
	}
	return n
}
func run(t *testing.T, f *Filter, q *dns.Msg) *dns.Msg {
	t.Helper()
	w := recorder()
	code, err := f.ServeDNS(context.Background(), w, q)
	if err != nil || w.msg == nil {
		t.Fatalf("ServeDNS: code=%d err=%v response=%v", code, err, w.msg)
	}
	return w.msg
}
func withAnswers(aaaa, a []string) *Filter {
	f := New(100*time.Millisecond, 16, 16)
	f.Next = handler(func(_ context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
		records := aaaa
		if q.Question[0].Qtype == dns.TypeA {
			records = a
		}
		m := answer(q, records...)
		if len(records) == 0 {
			m.Ns = []dns.RR{rr("test. 30 IN SOA ns.test. hostmaster.test. 1 60 60 60 30")}
		}
		return 0, w.WriteMsg(m)
	})
	return f
}

func TestSelectionAndRoundRobin(t *testing.T) {
	v4 := []string{"host.test. 90 IN A 192.0.2.1", "host.test. 80 IN A 192.0.2.2", "host.test. 70 IN A 192.0.2.3"}
	v6 := []string{"host.test. 60 IN AAAA 2001:db8::1", "host.test. 50 IN AAAA 2001:db8::2"}
	for _, tc := range []struct {
		name            string
		a, aaaa         []string
		wantA, wantAAAA int
	}{
		{"a_only", v4, nil, 3, 0}, {"aaaa_only", nil, v6, 0, 2}, {"dual", v4[:1], v6[:1], 1, 0},
		{"multiple_both", v4, v6, 3, 0}, {"neither", nil, nil, 0, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := withAnswers(tc.aaaa, tc.a)
			a := run(t, f, query("host.test.", dns.TypeA))
			aaaa := run(t, f, query("host.test.", dns.TypeAAAA))
			if countType(a, dns.TypeA) != tc.wantA || countType(aaaa, dns.TypeAAAA) != tc.wantAAAA {
				t.Fatalf("A=%v AAAA=%v", a, aaaa)
			}
			if countType(aaaa, dns.TypeA) != 0 || aaaa.Question[0].Qtype != dns.TypeAAAA || aaaa.Rcode != dns.RcodeSuccess {
				t.Fatal("invalid AAAA response")
			}
			for i, record := range tc.a {
				if a.Answer[i].String() != rr(record).String() {
					t.Fatal("A record/TTL changed")
				}
			}
			if tc.wantAAAA > 0 {
				for i, record := range tc.aaaa {
					if aaaa.Answer[i].String() != rr(record).String() {
						t.Fatal("AAAA record/TTL changed")
					}
				}
			}
			if len(tc.a) > 0 && len(tc.aaaa) > 0 && (len(aaaa.Ns) > 0 || aaaa.AuthenticatedData || aaaa.Authoritative) {
				t.Fatal("fabricated authenticated negative")
			}
		})
	}
}

func TestCNAMEChains(t *testing.T) {
	aliases := []string{"host.test. 60 IN CNAME middle.test.", "middle.test. 50 IN CNAME final.test."}
	for _, dual := range []bool{true, false} {
		t.Run(map[bool]string{true: "dual", false: "v6_only"}[dual], func(t *testing.T) {
			v6 := append(append([]string{}, aliases...), "final.test. 40 IN AAAA 2001:db8::1", "final.test. 30 IN AAAA 2001:db8::2")
			a := append([]string{}, aliases...)
			if dual {
				a = append(a, "final.test. 25 IN A 192.0.2.1", "final.test. 20 IN A 192.0.2.2")
			}
			f := withAnswers(v6, a)
			got := run(t, f, query("HOST.test.", dns.TypeAAAA))
			want := 2
			if dual {
				want = 0
			}
			if countType(got, dns.TypeAAAA) != want || countType(got, dns.TypeCNAME) != 2 {
				t.Fatal(got)
			}
			for i := 0; i < 2; i++ {
				if got.Answer[i].String() != rr(aliases[i]).String() {
					t.Fatal("CNAME/TTL changed")
				}
			}
			gotA := run(t, f, query("host.test.", dns.TypeA))
			if dual && countType(gotA, dns.TypeA) != 2 {
				t.Fatal("lost round-robin A records")
			}
		})
	}
}

func TestSplitCNAMEChain(t *testing.T) {
	f := withAnswers([]string{"host.test. 60 IN CNAME middle.test.", "middle.test. 50 IN CNAME final.test.", "final.test. 40 IN AAAA 2001:db8::1"}, nil)
	var names []string
	base := f.Next
	f.Next = handler(func(ctx context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
		if q.Question[0].Qtype != dns.TypeA {
			return base.ServeDNS(ctx, w, q)
		}
		names = append(names, q.Question[0].Name)
		switch q.Question[0].Name {
		case "host.test.":
			return 0, w.WriteMsg(answer(q, "host.test. 60 IN CNAME middle.test."))
		case "middle.test.":
			return 0, w.WriteMsg(answer(q, "middle.test. 50 IN CNAME final.test."))
		default:
			return 0, w.WriteMsg(answer(q, "final.test. 40 IN A 192.0.2.1"))
		}
	})
	got := run(t, f, query("host.test.", dns.TypeAAAA))
	if countType(got, dns.TypeAAAA) != 0 || strings.Join(names, ",") != "host.test.,middle.test.,final.test." {
		t.Fatalf("%v %v", got, names)
	}
}

func TestFailuresNeverSuppressIPv6(t *testing.T) {
	for _, kind := range []string{"servfail", "nxdomain", "timeout", "error", "truncated", "wrong_question", "wrong_class", "not_reply", "unrelated_a", "zero_a", "multicast_a", "broadcast_a", "cname_loop", "conflict", "a_with_cname", "different_terminal", "too_deep", "cross_response_loop"} {
		t.Run(kind, func(t *testing.T) {
			f := withAnswers([]string{"host.test. 60 IN AAAA 2001:db8::1"}, nil)
			f.Timeout = 20 * time.Millisecond
			base := f.Next
			f.Next = handler(func(ctx context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
				if q.Question[0].Qtype != dns.TypeA {
					return base.ServeDNS(ctx, w, q)
				}
				m := answer(q, "host.test. 60 IN A 192.0.2.1")
				switch kind {
				case "servfail":
					m.Rcode = dns.RcodeServerFailure
				case "nxdomain":
					m.Rcode = dns.RcodeNameError
				case "timeout":
					<-ctx.Done()
					return dns.RcodeServerFailure, ctx.Err()
				case "error":
					return dns.RcodeServerFailure, errors.New("upstream failed")
				case "truncated":
					m.Truncated = true
				case "wrong_question":
					m.Question[0].Name = "wrong.test."
				case "wrong_class":
					m.Question[0].Qclass = dns.ClassCHAOS
				case "not_reply":
					m.Response = false
				case "unrelated_a":
					m.Answer = []dns.RR{rr("unrelated.test. 60 IN A 192.0.2.1")}
				case "zero_a":
					m.Answer = []dns.RR{rr("host.test. 60 IN A 0.0.0.0")}
				case "multicast_a":
					m.Answer = []dns.RR{rr("host.test. 60 IN A 224.0.0.1")}
				case "broadcast_a":
					m.Answer = []dns.RR{rr("host.test. 60 IN A 255.255.255.255")}
				case "cname_loop":
					m.Answer = []dns.RR{rr("host.test. 60 IN CNAME other.test."), rr("other.test. 60 IN CNAME host.test.")}
				case "conflict":
					m.Answer = []dns.RR{rr("host.test. 60 IN CNAME x.test."), rr("host.test. 60 IN CNAME y.test.")}
				case "a_with_cname":
					m.Answer = append(m.Answer, rr("host.test. 60 IN CNAME x.test."))
				case "different_terminal":
					m.Answer = []dns.RR{rr("host.test. 60 IN CNAME other.test."), rr("other.test. 60 IN A 192.0.2.1")}
				case "too_deep":
					f.MaxChain = 1
					m.Answer = []dns.RR{rr("host.test. 60 IN CNAME middle.test."), rr("middle.test. 60 IN CNAME host.test.")}
				case "cross_response_loop":
					if q.Question[0].Name == "host.test." {
						m.Answer = []dns.RR{rr("host.test. 60 IN CNAME other.test.")}
					} else {
						m.Answer = []dns.RR{rr("other.test. 60 IN CNAME host.test.")}
					}
				}
				return 0, w.WriteMsg(m)
			})
			start := time.Now()
			got := run(t, f, query("host.test.", dns.TypeAAAA))
			if countType(got, dns.TypeAAAA) != 1 || time.Since(start) > 250*time.Millisecond {
				t.Fatalf("lost IPv6 or excessive latency: %v", got)
			}
		})
	}
}

func TestDNSSECAndSyntheticSemantics(t *testing.T) {
	for _, mode := range []string{"do_bypass", "cd_bypass", "explicit_filter", "ordinary_filter"} {
		t.Run(mode, func(t *testing.T) {
			f := withAnswers([]string{"host.test. 60 IN AAAA 2001:db8::1"}, []string{"host.test. 60 IN A 192.0.2.1"})
			base := f.Next
			var calls atomic.Int32
			f.Next = handler(func(ctx context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
				if q.Question[0].Qtype == dns.TypeA {
					calls.Add(1)
					return base.ServeDNS(ctx, w, q)
				}
				m := answer(q, "host.test. 60 IN AAAA 2001:db8::1", "host.test. 60 IN RRSIG AAAA 8 2 60 20300101000000 20200101000000 123 test. AA==")
				m.AuthenticatedData = true
				m.Authoritative = true
				m.Ns = []dns.RR{rr("test. 60 IN SOA ns.test. hostmaster.test. 1 60 60 60 60")}
				m.Extra = []dns.RR{rr("glue.test. 60 IN AAAA 2001:db8::2")}
				return 0, w.WriteMsg(m)
			})
			q := query("host.test.", dns.TypeAAAA)
			q.SetEdns0(1232, mode != "ordinary_filter")
			q.CheckingDisabled = mode == "cd_bypass"
			f.FilterDNSSEC = mode == "explicit_filter"
			got := run(t, f, q)
			if strings.HasSuffix(mode, "bypass") {
				if !got.AuthenticatedData || countType(got, dns.TypeAAAA) != 1 || calls.Load() != 0 {
					t.Fatal(got)
				}
			} else {
				if got.AuthenticatedData || got.Authoritative || len(got.Answer) != 0 || len(got.Ns) != 0 || calls.Load() != 1 {
					t.Fatal(got)
				}
				opt := got.IsEdns0()
				if len(got.Extra) != 1 || opt == nil || opt.Do() || len(opt.Option) != 1 || opt.Option[0].(*dns.EDNS0_EDE).InfoCode != dns.ExtendedErrorCodeFiltered {
					t.Fatal("missing policy EDE or leaked DNSSEC/glue")
				}
			}
		})
	}
}

func TestOtherTypesAndNegativeResponses(t *testing.T) {
	for _, record := range []string{"host.test. 60 IN TXT \"hello\"", "host.test. 60 IN MX 10 mail.test.", "host.test. 60 IN SRV 1 2 443 service.test.", "host.test. 60 IN PTR target.test.", "host.test. 60 IN HTTPS 1 . ipv6hint=2001:db8::1", "host.test. 60 IN SVCB 1 . ipv6hint=2001:db8::2"} {
		r := rr(record)
		f := New(time.Second, 16, 8)
		f.Next = handler(func(_ context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
			return 0, w.WriteMsg(answer(q, record))
		})
		got := run(t, f, query("host.test.", r.Header().Rrtype))
		if len(got.Answer) != 1 || got.Answer[0].String() != r.String() {
			t.Fatal("changed other record", got)
		}
	}
	for _, rcode := range []int{dns.RcodeNameError, dns.RcodeSuccess, dns.RcodeServerFailure} {
		f := New(time.Second, 16, 8)
		var calls int
		f.Next = handler(func(_ context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
			calls++
			m := answer(q)
			m.Rcode = rcode
			m.Ns = []dns.RR{rr("test. 23 IN SOA ns.test. hostmaster.test. 1 60 60 60 17")}
			return 0, w.WriteMsg(m)
		})
		got := run(t, f, query("host.test.", dns.TypeAAAA))
		if got.Rcode != rcode || len(got.Answer) != 0 || got.Ns[0].Header().Ttl != 23 || calls != 1 {
			t.Fatal("negative response changed", got)
		}
	}
}

func TestCoalescedConcurrentRequests(t *testing.T) {
	f := withAnswers([]string{"host.test. 60 IN AAAA 2001:db8::1"}, []string{"host.test. 60 IN A 192.0.2.1"})
	f.Timeout = time.Second
	base := f.Next
	var calls atomic.Int32
	f.Next = handler(func(ctx context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
		if q.Question[0].Qtype == dns.TypeA {
			calls.Add(1)
			time.Sleep(80 * time.Millisecond)
		}
		return base.ServeDNS(ctx, w, q)
	})
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			got := run(t, f, query("host.test.", dns.TypeAAAA))
			if countType(got, dns.TypeAAAA) != 0 {
				t.Error("unexpected IPv6")
			}
		}()
	}
	close(start)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("A lookups=%d, want one coalesced lookup", calls.Load())
	}
}

func TestBoundedProbeConcurrency(t *testing.T) {
	f := New(30*time.Millisecond, 16, 1)
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	f.Next = handler(func(_ context.Context, w dns.ResponseWriter, q *dns.Msg) (int, error) {
		if q.Question[0].Qtype == dns.TypeA {
			entered <- struct{}{}
			<-release
			return 0, w.WriteMsg(answer(q, q.Question[0].Name+" 30 IN A 192.0.2.1"))
		}
		return 0, w.WriteMsg(answer(q, q.Question[0].Name+" 30 IN AAAA 2001:db8::1"))
	})
	done := make(chan struct{})
	go func() { run(t, f, query("one.test.", dns.TypeAAAA)); close(done) }()
	<-entered
	got := run(t, f, query("two.test.", dns.TypeAAAA))
	if countType(got, dns.TypeAAAA) != 1 {
		t.Fatal("saturation did not preserve IPv6")
	}
	close(release)
	<-done
}

func TestParse(t *testing.T) {
	for _, config := range []string{"prefer_ipv4", "prefer_ipv4 {\n lookup_timeout 250ms\n max_chain 8\n max_concurrent 32\n dnssec filter\n}"} {
		if _, err := parse(caddy.NewTestController("dns", config)); err != nil {
			t.Fatal(err)
		}
	}
	for _, config := range []string{"prefer_ipv4 x", "prefer_ipv4\nprefer_ipv4", "prefer_ipv4 {\n lookup_timeout 0s\n}", "prefer_ipv4 {\n max_chain -1\n}", "prefer_ipv4 {\n max_concurrent 0\n}", "prefer_ipv4 {\n dnssec yes\n}", "prefer_ipv4 {\n unknown x\n}", "prefer_ipv4 {\n dnssec bypass\n dnssec filter\n}"} {
		if _, err := parse(caddy.NewTestController("dns", config)); err == nil {
			t.Errorf("accepted %q", config)
		}
	}
}

var _ plugin.Handler = (*Filter)(nil)
