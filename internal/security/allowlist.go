package security

import (
	"net"
	"net/url"
	"strings"
)

func ContainsInt64(items []int64, v int64) bool {
	for _, item := range items {
		if item == v {
			return true
		}
	}
	return false
}

func IsAllowedHost(rawURL string, allowed []string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if ip := net.ParseIP(host); ip != nil {
		return false
	}
	for _, item := range allowed {
		item = strings.ToLower(strings.TrimSpace(item))
		if item == "" {
			continue
		}
		if host == item || strings.HasSuffix(host, "."+item) {
			return true
		}
	}
	return false
}
