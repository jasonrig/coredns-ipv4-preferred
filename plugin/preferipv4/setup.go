package preferipv4

import (
	"strconv"
	"time"

	"github.com/coredns/caddy"
	"github.com/coredns/coredns/core/dnsserver"
	"github.com/coredns/coredns/plugin"
)

func init() { plugin.Register("prefer_ipv4", setup) }

func setup(c *caddy.Controller) error {
	f, err := parse(c)
	if err != nil {
		return plugin.Error("prefer_ipv4", err)
	}
	dnsserver.GetConfig(c).AddPlugin(func(next plugin.Handler) plugin.Handler { f.Next = next; return f })
	return nil
}

func parse(c *caddy.Controller) (*Filter, error) {
	timeout, chain, concurrency, filterDNSSEC := 500*time.Millisecond, 16, 64, false
	count := 0
	for c.Next() {
		count++
		if count != 1 {
			return nil, plugin.ErrOnce
		}
		if len(c.RemainingArgs()) != 0 {
			return nil, c.ArgErr()
		}
		seen := make(map[string]bool)
		for c.NextBlock() {
			name := c.Val()
			if seen[name] {
				return nil, c.Errf("duplicate option %s", name)
			}
			seen[name] = true
			args := c.RemainingArgs()
			if len(args) != 1 {
				return nil, c.ArgErr()
			}
			switch name {
			case "lookup_timeout":
				v, err := time.ParseDuration(args[0])
				if err != nil || v < time.Millisecond || v > 5*time.Second {
					return nil, c.Err("lookup_timeout must be 1ms through 5s")
				}
				timeout = v
			case "max_chain", "max_concurrent":
				v, err := strconv.Atoi(args[0])
				if err != nil || v < 1 || v > 256 {
					return nil, c.Err("limits must be 1 through 256")
				}
				if name == "max_chain" {
					chain = v
				} else {
					concurrency = v
				}
			case "dnssec":
				if args[0] != "bypass" && args[0] != "filter" {
					return nil, c.Err("dnssec must be bypass or filter")
				}
				filterDNSSEC = args[0] == "filter"
			default:
				return nil, c.Errf("unknown option %s", name)
			}
		}
	}
	f := New(timeout, chain, concurrency)
	f.FilterDNSSEC = filterDNSSEC
	return f, nil
}
