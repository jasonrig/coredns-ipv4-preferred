// A small CoreDNS distribution: only the middleware used by this appliance.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/coremain"
	_ "github.com/coredns/coredns/plugin/bind"
	_ "github.com/coredns/coredns/plugin/cache"
	_ "github.com/coredns/coredns/plugin/errors"
	_ "github.com/coredns/coredns/plugin/forward"
	_ "github.com/coredns/coredns/plugin/health"
	_ "github.com/coredns/coredns/plugin/log"
	_ "github.com/coredns/coredns/plugin/loop"
	_ "github.com/coredns/coredns/plugin/metrics"
	_ "github.com/coredns/coredns/plugin/ready"
	_ "github.com/coredns/coredns/plugin/reload"
	_ "github.com/jasonrig/coredns-ipv4-preferred/plugin/preferipv4"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "-healthcheck" {
		client := http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://127.0.0.1:8181/ready")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			fmt.Fprintln(os.Stderr, resp.Status)
			os.Exit(1)
		}
		return
	}
	// Corefile text order does not control middleware order. In particular, keep
	// policy OUTSIDE the raw-answer cache, on both the query and response paths.
	dnsserver.Directives = []string{"bind", "errors", "log", "health", "ready", "prometheus", "reload", "prefer_ipv4", "cache", "loop", "forward"}
	coremain.Run()
}
