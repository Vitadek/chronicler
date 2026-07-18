package auth

import (
	"net"
	"strings"
)

var presets = map[string][]string{
	"loopback":    {"127.0.0.0/8", "::1/128"},
	"linklocal":   {"169.254.0.0/16", "fe80::/10"},
	"uniquelocal": {"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fc00::/7"},
}

type IPFilter struct {
	exactIPs  []net.IP
	networks  []*net.IPNet
}

func ParseTrustedProxies(spec string) *IPFilter {
	filter := &IPFilter{}
	if spec == "" {
		return filter
	}

	parts := strings.Split(spec, ",")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		if rawList, exists := presets[p]; exists {
			for _, r := range rawList {
				filter.addCIDROrIP(r)
			}
		} else {
			filter.addCIDROrIP(p)
		}
	}
	return filter
}

func (f *IPFilter) addCIDROrIP(spec string) {
	if strings.Contains(spec, "/") {
		_, ipNet, err := net.ParseCIDR(spec)
		if err == nil {
			f.networks = append(f.networks, ipNet)
		}
	} else {
		ip := net.ParseIP(spec)
		if ip != nil {
			f.exactIPs = append(f.exactIPs, ip)
		}
	}
}

func (f *IPFilter) Matches(ipStr string) bool {
	// Clean up port if present
	if strings.Contains(ipStr, ":") {
		host, _, err := net.SplitHostPort(ipStr)
		if err == nil {
			ipStr = host
		}
	}

	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	// Canonicalize IPv4-mapped IPv6 addresses (::ffff:127.0.0.1 -> 127.0.0.1)
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	for _, exact := range f.exactIPs {
		exactIP := exact
		if exactIP4 := exactIP.To4(); exactIP4 != nil {
			exactIP = exactIP4
		}
		if ip.Equal(exactIP) {
			return true
		}
	}

	for _, network := range f.networks {
		if network.Contains(ip) {
			return true
		}
	}

	return false
}
