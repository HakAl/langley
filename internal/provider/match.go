package provider

import "strings"

// MatchDomainSuffix reports whether host (with optional :port) matches the
// given domain suffix. It performs case-insensitive comparison and requires an
// exact match or a subdomain boundary (dot-separated).
//
// Examples:
//
//	MatchDomainSuffix("api.anthropic.com", "anthropic.com")   => true
//	MatchDomainSuffix("anthropic.com:443", "anthropic.com")   => true
//	MatchDomainSuffix("misanthropic.com",  "anthropic.com")   => false
func MatchDomainSuffix(host, suffix string) bool {
	// Strip port if present
	if i := strings.LastIndex(host, ":"); i != -1 {
		host = host[:i]
	}

	host = strings.ToLower(host)
	suffix = strings.ToLower(suffix)

	if host == suffix {
		return true
	}

	// Must end with "."+suffix to be a subdomain match
	return strings.HasSuffix(host, "."+suffix)
}
