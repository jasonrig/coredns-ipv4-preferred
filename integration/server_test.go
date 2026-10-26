package integration_test

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/jasonrig/coredns-ipv4-preferred/internal/fixtures"
	"github.com/miekg/dns"
)

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "coredns-integration-")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "coredns")
	cmd := exec.Command("go", "build", "-buildvcs=false", "-o", binary, "../cmd/coredns")
	if b, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n%s", err, b)
		_ = os.RemoveAll(dir)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func freePort(t *testing.T) int {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	p := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return p
}
func upstream(t *testing.T) (*fixtures.Resolver, string) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	u, err := net.ListenPacket("udp", addr)
	if err != nil {
		_ = l.Close()
		t.Fatal(err)
	}
	r := fixtures.New()
	a := &dns.Server{Listener: l, Net: "tcp", Handler: r}
	b := &dns.Server{PacketConn: u, Net: "udp", Handler: r}
	go func() { _ = a.ActivateAndServe() }()
	go func() { _ = b.ActivateAndServe() }()
	t.Cleanup(func() { _ = a.Shutdown(); _ = b.Shutdown() })
	return r, addr
}
func start(t *testing.T, upstream string) (string, string) {
	t.Helper()
	dnsPort, readyPort, healthPort, metricsPort := freePort(t), freePort(t), freePort(t), freePort(t)
	config := fmt.Sprintf(`.:%d {
 bind 127.0.0.1
 errors
 ready 127.0.0.1:%d
 health 127.0.0.1:%d
 prometheus 127.0.0.1:%d
 prefer_ipv4 {
  lookup_timeout 120ms
  max_chain 16
  max_concurrent 64
 }
 cache 300 {
  success 4096 300 0
  denial 1024 60 0
  servfail 0
 }
 forward . %s {
  max_connect_attempts 1
  max_fails 0
  read_timeout 500ms
 }
}
`, dnsPort, readyPort, healthPort, metricsPort, upstream)
	return startConfig(t, config, dnsPort, readyPort, metricsPort, nil)
}

func startConfig(t *testing.T, config string, dnsPort, readyPort, metricsPort int, environment []string) (string, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "Corefile")
	if e := os.WriteFile(path, []byte(config), 0600); e != nil {
		t.Fatal(e)
	}
	var logs bytes.Buffer
	cmd := exec.Command(binary, "-conf", path)
	cmd.Env = append(os.Environ(), environment...)
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if e := cmd.Start(); e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(4 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		if t.Failed() {
			t.Log(logs.String())
		}
	})
	url := fmt.Sprintf("http://127.0.0.1:%d/ready", readyPort)
	client := http.Client{Timeout: 100 * time.Millisecond}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		resp, e := client.Get(url)
		if e == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == 200 {
				return fmt.Sprintf("127.0.0.1:%d", dnsPort), fmt.Sprintf("http://127.0.0.1:%d/metrics", metricsPort)
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("CoreDNS did not become ready")
	return "", ""
}
func ask(t *testing.T, addr, proto, name string, typ uint16) *dns.Msg {
	t.Helper()
	q := new(dns.Msg)
	q.SetQuestion(name, typ)
	q.SetEdns0(1232, false)
	m, _, err := (&dns.Client{Net: proto, Timeout: 2 * time.Second}).Exchange(q, addr)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
func count(m *dns.Msg, typ uint16) int {
	n := 0
	for _, r := range m.Answer {
		if r.Header().Rrtype == typ {
			n++
		}
	}
	return n
}

func TestRunningServerUDPAndTCP(t *testing.T) {
	_, up := upstream(t)
	addr, metricsURL := start(t, up)
	for _, proto := range []string{"udp", "tcp"} {
		t.Run(proto, func(t *testing.T) {
			for _, tc := range []struct {
				name        string
				typ         uint16
				want, rcode int
			}{
				{"v4.test.", dns.TypeA, 3, 0}, {"v4.test.", dns.TypeAAAA, 0, 0},
				{"v6.test.", dns.TypeAAAA, 2, 0}, {"dual.test.", dns.TypeA, 3, 0}, {"dual.test.", dns.TypeAAAA, 0, 0},
				{"alias.test.", dns.TypeAAAA, 0, 0}, {"alias6.test.", dns.TypeAAAA, 2, 0},
				{"multi.test.", dns.TypeAAAA, 0, 0}, {"split.test.", dns.TypeAAAA, 0, 0},
				{"large.test.", dns.TypeAAAA, 0, 0},
				{"missing.test.", dns.TypeAAAA, 0, dns.RcodeNameError}, {"nodata.test.", dns.TypeAAAA, 0, 0},
				{"servfail.test.", dns.TypeAAAA, 2, 0}, {"timeout.test.", dns.TypeAAAA, 2, 0},
				{"failure.test.", dns.TypeAAAA, 0, dns.RcodeServerFailure},
				{"other.test.", dns.TypeTXT, 1, 0}, {"other.test.", dns.TypeMX, 1, 0}, {"other.test.", dns.TypeSRV, 1, 0},
				{"other.test.", dns.TypePTR, 1, 0}, {"other.test.", dns.TypeHTTPS, 1, 0}, {"other.test.", dns.TypeSVCB, 1, 0},
			} {
				t.Run(tc.name+dns.TypeToString[tc.typ], func(t *testing.T) {
					before := time.Now()
					got := ask(t, addr, proto, tc.name, tc.typ)
					if got.Rcode != tc.rcode || count(got, tc.typ) != tc.want {
						t.Fatalf("%s: %v", proto, got)
					}
					if tc.name == "timeout.test." && time.Since(before) > 300*time.Millisecond {
						t.Fatal("A proof exceeded its latency budget")
					}
					if (tc.name == "multi.test." || tc.name == "split.test.") && count(got, dns.TypeCNAME) != 2 {
						t.Fatal("lost CNAME chain")
					}
				})
			}
		})
	}
	resp, err := http.Get(metricsURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatal("metrics unavailable")
	}
}

func TestRawCacheAndExpiry(t *testing.T) {
	r, up := upstream(t)
	addr, _ := start(t, up)
	first := ask(t, addr, "udp", "cache.test.", dns.TypeA)
	if count(first, dns.TypeA) != 3 {
		t.Fatal(first)
	}
	for i := 0; i < 10; i++ {
		if count(ask(t, addr, "udp", "cache.test.", dns.TypeAAAA), dns.TypeAAAA) != 0 {
			t.Fatal("not suppressed")
		}
	}
	if r.Count("cache.test.", dns.TypeA) != 1 || r.Count("cache.test.", dns.TypeAAAA) != 1 {
		t.Fatal("raw cache was bypassed")
	}
	time.Sleep(1100 * time.Millisecond)
	second := ask(t, addr, "tcp", "cache.test.", dns.TypeA)
	if second.Answer[0].Header().Ttl >= first.Answer[0].Header().Ttl {
		t.Fatal("cache TTL did not decrease")
	}
	if count(ask(t, addr, "udp", "change.test.", dns.TypeAAAA), dns.TypeAAAA) != 0 {
		t.Fatal("initial A proof missing")
	}
	r.NoIPv4.Store(true)
	time.Sleep(1100 * time.Millisecond)
	if count(ask(t, addr, "udp", "change.test.", dns.TypeAAAA), dns.TypeAAAA) != 2 {
		t.Fatal("filtered negative or stale A proof survived expiry")
	}
	if r.Count("change.test.", dns.TypeAAAA) != 1 || r.Count("change.test.", dns.TypeA) != 2 {
		t.Fatal("raw AAAA was not reused / A proof not refreshed")
	}
}

func TestConcurrentNetworkRequests(t *testing.T) {
	_, up := upstream(t)
	addr, _ := start(t, up)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			proto := "udp"
			if i%2 == 0 {
				proto = "tcp"
			}
			m := ask(t, addr, proto, "concurrent.test.", dns.TypeAAAA)
			if count(m, dns.TypeAAAA) != 0 {
				t.Error("lost policy under concurrency")
			}
		}(i)
	}
	wg.Wait()
}

func TestDNSSECBypassOnWire(t *testing.T) {
	_, up := upstream(t)
	addr, _ := start(t, up)
	q := new(dns.Msg)
	q.SetQuestion("dual.test.", dns.TypeAAAA)
	q.SetEdns0(1232, true)
	m, _, err := (&dns.Client{Timeout: time.Second}).Exchange(q, addr)
	if err != nil || count(m, dns.TypeAAAA) != 2 {
		t.Fatalf("DO bypass: %v %v", m, err)
	}
	q.CheckingDisabled = true
	q.IsEdns0().SetDo(false)
	m, _, err = (&dns.Client{Net: "tcp", Timeout: time.Second}).Exchange(q, addr)
	if err != nil || count(m, dns.TypeAAAA) != 2 {
		t.Fatalf("CD bypass: %v %v", m, err)
	}
}
