// Package preferipv4 conditionally removes AAAA addresses when a successful A
// lookup proves that the same canonical destination has a usable IPv4 address.
package preferipv4

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/coredns/coredns/plugin"
	"github.com/miekg/dns"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"golang.org/x/sync/singleflight"
)

var decisions = promauto.NewCounterVec(prometheus.CounterOpts{
	Namespace: "coredns", Subsystem: "prefer_ipv4", Name: "decisions_total",
	Help: "AAAA policy decisions, without query names or client identifiers.",
}, []string{"result"})

// Filter is a CoreDNS middleware. Its Next chain must cache raw, not filtered,
// answers. Construct it with New, then set Next before serving requests.
type Filter struct {
	Next         plugin.Handler
	Timeout      time.Duration
	MaxChain     int
	FilterDNSSEC bool
	probes       chan struct{}
	flight       singleflight.Group
}

// New returns a filter with bounded companion lookups and DNSSEC bypass enabled.
func New(timeout time.Duration, maxChain, maxConcurrent int) *Filter {
	return &Filter{Timeout: timeout, MaxChain: maxChain, probes: make(chan struct{}, maxConcurrent)}
}

// Name implements plugin.Handler.
func (*Filter) Name() string { return "prefer_ipv4" }

// ServeDNS preserves all non-AAAA requests and fails open on inconclusive proof.
func (f *Filter) ServeDNS(ctx context.Context, w dns.ResponseWriter, req *dns.Msg) (int, error) {
	if len(req.Question) != 1 || req.Opcode != dns.OpcodeQuery || req.Response ||
		req.Question[0].Qtype != dns.TypeAAAA || req.Question[0].Qclass != dns.ClassINET || req.IsTsig() != nil {
		return plugin.NextOrFailure(f.Name(), f.Next, ctx, w, req)
	}
	if !f.FilterDNSSEC && (req.CheckingDisabled || (req.IsEdns0() != nil && req.IsEdns0().Do())) {
		decisions.WithLabelValues("dnssec_bypass").Inc()
		return plugin.NextOrFailure(f.Name(), f.Next, ctx, w, req)
	}
	// Nonstandard EDNS options can change upstream views (e.g. ECS). Avoid making
	// a cross-query decision in that case; this appliance is not an ECS resolver.
	if opt := req.IsEdns0(); opt != nil {
		for _, option := range opt.Option {
			switch option.Option() {
			case dns.EDNS0COOKIE, dns.EDNS0PADDING, dns.EDNS0NSID, dns.EDNS0TCPKEEPALIVE:
				// Transport/diagnostic options do not change address selection.
			default:
				decisions.WithLabelValues("edns_bypass").Inc()
				return plugin.NextOrFailure(f.Name(), f.Next, ctx, w, req)
			}
		}
	}
	buf := newCapture(w.LocalAddr(), w.RemoteAddr())
	code, err := plugin.NextOrFailure(f.Name(), f.Next, ctx, buf, req)
	if buf.msg == nil {
		return code, err
	}
	orig := buf.msg
	if err != nil || !validResponse(orig, req) {
		return code, writeResponse(w, orig, err)
	}
	path, pathErr := inspect(orig, req.Question[0].Name, dns.TypeAAAA, f.MaxChain)
	if pathErr != nil || !path.found {
		decisions.WithLabelValues("unchanged").Inc()
		return code, w.WriteMsg(orig)
	}
	proof := f.proof(ctx, w, req, path.terminal)
	decisions.WithLabelValues(proof).Inc()
	if proof != "suppressed" {
		return code, w.WriteMsg(orig)
	}
	return code, w.WriteMsg(filtered(req, orig, path.aliases))
}

func writeResponse(w dns.ResponseWriter, msg *dns.Msg, previous error) error {
	if err := w.WriteMsg(msg); err != nil {
		return err
	}
	return previous
}

func (f *Filter) proof(ctx context.Context, w dns.ResponseWriter, req *dns.Msg, terminal string) string {
	q := req.Copy()
	q.Id = 0 // Randomized by forward; also makes identical queries coalesce.
	q.Question[0].Qtype = dns.TypeA
	q.Question[0].Name = canonical(q.Question[0].Name)
	packed, err := q.Pack()
	if err != nil {
		return "inconclusive"
	}
	// Preserve query flags/EDNS in the key. Keep TCP/UDP and client IP views apart.
	host, _, _ := net.SplitHostPort(w.RemoteAddr().String())
	key := string(packed) + "|" + terminal + "|" + w.LocalAddr().Network() + "|" + host
	local, remote := w.LocalAddr(), w.RemoteAddr()
	result := f.flight.DoChan(key, func() (any, error) {
		select {
		case f.probes <- struct{}{}:
		default:
			return "inconclusive", nil
		}
		defer func() { <-f.probes }()
		// A cancelled first caller must not cancel other coalesced callers. This
		// separate deadline bounds the entire chain, not each individual hop.
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), f.Timeout)
		defer cancel()
		return f.lookupA(probeCtx, local, remote, q, terminal), nil
	})
	timer := time.NewTimer(f.Timeout)
	defer timer.Stop()
	select {
	case res := <-result:
		if res.Err != nil {
			return "inconclusive"
		}
		return res.Val.(string)
	case <-timer.C:
		return "inconclusive"
	case <-ctx.Done():
		return "inconclusive"
	}
}

func (f *Filter) lookupA(ctx context.Context, local, remote net.Addr, q *dns.Msg, expected string) string {
	seen := make(map[string]bool)
	remaining := f.MaxChain
	for {
		if ctx.Err() != nil {
			return "inconclusive"
		}
		name := canonical(q.Question[0].Name)
		if seen[name] {
			return "inconclusive"
		}
		buf := newCapture(local, remote)
		_, err := plugin.NextOrFailure(f.Name(), f.Next, ctx, buf, q)
		if err == nil && buf.msg != nil && buf.msg.Truncated {
			// A large round-robin set can require TCP even for a UDP client.
			// Never make the decision from a truncated A answer.
			tcpLocal, e1 := net.ResolveTCPAddr("tcp", local.String())
			tcpRemote, e2 := net.ResolveTCPAddr("tcp", remote.String())
			if e1 != nil || e2 != nil {
				return "inconclusive"
			}
			buf = newCapture(tcpLocal, tcpRemote)
			_, err = plugin.NextOrFailure(f.Name(), f.Next, ctx, buf, q)
		}
		if err != nil || !validResponse(buf.msg, q) || ctx.Err() != nil {
			return "inconclusive"
		}
		path, err := inspect(buf.msg, name, dns.TypeA, remaining)
		if err != nil {
			return "inconclusive"
		}
		for _, alias := range path.aliases {
			owner := canonical(alias.Header().Name)
			if seen[owner] {
				return "inconclusive"
			}
			seen[owner] = true
		}
		remaining -= len(path.aliases)
		if path.found {
			if path.terminal == expected {
				return "suppressed"
			}
			return "inconclusive"
		}
		// An enclosing SOA can already confirm terminal NODATA after a CNAME;
		// do not issue a redundant query for that same negative destination.
		for _, rr := range buf.msg.Ns {
			if rr.Header().Rrtype == dns.TypeSOA && rr.Header().Class == dns.ClassINET && dns.IsSubDomain(canonical(rr.Header().Name), path.terminal) {
				return "no_a"
			}
		}
		if len(path.aliases) == 0 {
			return "inconclusive"
		}
		// Some recursive upstreams return only part of the CNAME chain. Continue
		// at its terminal name through the same cache/forward chain.
		q = q.Copy()
		q.Question[0].Name = path.terminal
	}
}

type chain struct {
	terminal string
	aliases  []dns.RR
	found    bool
}

func inspect(msg *dns.Msg, start string, typ uint16, limit int) (chain, error) {
	out := chain{terminal: canonical(start)}
	visited := make(map[string]bool)
	for {
		name := out.terminal
		if visited[name] {
			return out, errors.New("CNAME loop")
		}
		visited[name] = true
		var alias *dns.CNAME
		hasAddress := false
		for _, rr := range msg.Answer {
			if rr == nil {
				return out, errors.New("nil RR")
			}
			if rr.Header().Rrtype == dns.TypeDNAME {
				return out, errors.New("DNAME is not rewritten")
			}
			if canonical(rr.Header().Name) != name {
				continue
			}
			if rr.Header().Class != dns.ClassINET {
				return out, errors.New("wrong RR class")
			}
			switch v := rr.(type) {
			case *dns.CNAME:
				if _, ok := dns.IsDomainName(v.Target); !ok {
					return out, errors.New("invalid CNAME target")
				}
				if alias != nil && canonical(alias.Target) != canonical(v.Target) {
					return out, errors.New("conflicting CNAMEs")
				}
				alias = v
			case *dns.A:
				if typ == dns.TypeA && usableIPv4(v.A) {
					out.found = true
				}
				hasAddress = true
			case *dns.AAAA:
				if typ == dns.TypeAAAA && v.AAAA.To16() != nil && v.AAAA.To4() == nil {
					out.found = true
				}
				hasAddress = true
			}
		}
		if alias == nil {
			return out, nil
		}
		if hasAddress {
			return out, errors.New("CNAME and address coexist")
		}
		if len(out.aliases) >= limit {
			return out, errors.New("CNAME depth exceeded")
		}
		out.aliases = append(out.aliases, alias)
		out.terminal = canonical(alias.Target)
	}
}

func canonical(name string) string { return strings.ToLower(dns.Fqdn(name)) }

func usableIPv4(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	a := netip.AddrFrom4([4]byte(v4))
	return !a.IsUnspecified() && !a.IsMulticast() && a.String() != "255.255.255.255"
}

func validResponse(msg, req *dns.Msg) bool {
	return msg != nil && msg.Response && msg.Opcode == dns.OpcodeQuery &&
		msg.Rcode == dns.RcodeSuccess && !msg.Truncated && len(msg.Question) == 1 &&
		canonical(msg.Question[0].Name) == canonical(req.Question[0].Name) &&
		msg.Question[0].Qtype == req.Question[0].Qtype && msg.Question[0].Qclass == req.Question[0].Qclass
}

func filtered(req, orig *dns.Msg, aliases []dns.RR) *dns.Msg {
	out := orig.Copy()
	out.Authoritative, out.AuthenticatedData = false, false
	out.Answer = nil
	for _, rr := range aliases {
		out.Answer = append(out.Answer, dns.Copy(rr))
	}
	// This is policy, not authoritative denial of existence. Never fabricate an
	// SOA/NSEC proof or leave unrelated AAAA glue/signatures in the answer.
	out.Ns, out.Extra = nil, nil
	if requestOPT := req.IsEdns0(); requestOPT != nil {
		if originalOPT := orig.IsEdns0(); originalOPT != nil {
			opt := dns.Copy(originalOPT).(*dns.OPT)
			opt.SetDo(false)
			opt.Option = nil
			for _, option := range originalOPT.Option {
				if option.Option() != dns.EDNS0EDE {
					opt.Option = append(opt.Option, option)
				}
			}
			out.Extra = []dns.RR{opt}
		} else {
			out.SetEdns0(requestOPT.UDPSize(), false)
		}
		out.IsEdns0().Option = append(out.IsEdns0().Option, &dns.EDNS0_EDE{
			InfoCode: dns.ExtendedErrorCodeFiltered, ExtraText: "IPv4 preferred: usable A record exists",
		})
	}
	return out
}

// capture detaches internal lookups from the actual client socket. A timed-out
// probe can finish safely without writing to or retaining the client writer.
type capture struct {
	local, remote net.Addr
	msg           *dns.Msg
}

func newCapture(local, remote net.Addr) *capture { return &capture{local: local, remote: remote} }
func (c *capture) LocalAddr() net.Addr           { return c.local }
func (c *capture) RemoteAddr() net.Addr          { return c.remote }
func (c *capture) WriteMsg(msg *dns.Msg) error {
	if c.msg != nil {
		return fmt.Errorf("multiple responses are unsupported")
	}
	c.msg = msg.Copy()
	return nil
}
func (c *capture) Write(b []byte) (int, error) {
	msg := new(dns.Msg)
	if err := msg.Unpack(b); err != nil {
		return 0, err
	}
	if err := c.WriteMsg(msg); err != nil {
		return 0, err
	}
	return len(b), nil
}
func (*capture) Close() error        { return nil }
func (*capture) TsigStatus() error   { return nil }
func (*capture) TsigTimersOnly(bool) {}
func (*capture) Hijack()             {}
