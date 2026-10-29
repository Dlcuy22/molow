package loginwin

import (
	"sort"
	"strings"
)

// Domain priorities. A cookie scoped to the exact host beats the same name from
// a parent domain, because GenerateSAPISIDHash hashes against
// https://music.youtube.com, so the SAPISID it needs is the one scoped there.
const (
	priorityExactHost = iota
	priorityYouTube
	priorityGoogle
	priorityNone
)

// domainPriority maps a cookie domain to its selection order.
func domainPriority(domain string) int {
	d := strings.ToLower(strings.TrimPrefix(domain, "."))
	switch d {
	case "music.youtube.com":
		return priorityExactHost
	case "youtube.com":
		return priorityYouTube
	case "google.com":
		return priorityGoogle
	}
	return priorityNone
}

// cookieHeader builds the single Cookie: header value ytm-go replays verbatim.
//
// The jar cannot be used as it comes out of WebKit: the sign-in flow passes
// through accounts.google.com, so the same name exists in several scopes and
// get_all_cookies promises no ordering. Two SAPISID entries would leave the
// parser keeping whichever it met last, so auth would depend on libsoup's
// internal order.
func cookieHeader(cookies []Cookie) string {
	kept := make([]Cookie, 0, len(cookies))
	for _, c := range cookies {
		if c.Name == "" {
			continue
		}
		if domainPriority(c.Domain) == priorityNone {
			continue
		}
		// A path-scoped cookie belongs to one endpoint, never to the origin.
		if c.Path != "/" {
			continue
		}
		kept = append(kept, c)
	}

	sort.SliceStable(kept, func(i, j int) bool {
		pi, pj := domainPriority(kept[i].Domain), domainPriority(kept[j].Domain)
		if pi != pj {
			return pi < pj
		}
		return kept[i].Name < kept[j].Name
	})

	var b strings.Builder
	seen := make(map[string]bool, len(kept))
	for _, c := range kept {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		if b.Len() > 0 {
			b.WriteString("; ")
		}
		b.WriteString(c.Name)
		b.WriteByte('=')
		b.WriteString(c.Value)
	}
	return b.String()
}

// HasSessionCookies reports whether a Cookie header can authenticate anything.
//
// A SAPISID alone cannot: it only seeds the Authorization hash, while the session
// identity comes from the primary cookies. Measured against the live endpoint, a
// header with SAPISID but no SID or __Secure-3PSID returns the signed-out page.
func HasSessionCookies(header string) bool {
	var hasSeed, hasIdentity bool
	for _, part := range strings.Split(header, ";") {
		name, _, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(name) {
		case "SAPISID", "__Secure-3PAPISID", "__Secure-1PAPISID":
			hasSeed = true
		case "SID", "__Secure-3PSID", "__Secure-1PSID":
			hasIdentity = true
		}
	}
	return hasSeed && hasIdentity
}

// sapiSID returns the SAPISID value from a cookie set, preferring the exact-host
// scope the same way cookieHeader does.
func sapiSID(cookies []Cookie) string {
	best := ""
	bestPriority := priorityNone
	for _, c := range cookies {
		if c.Name != "SAPISID" && c.Name != "__Secure-3PAPISID" {
			continue
		}
		if p := domainPriority(c.Domain); p < bestPriority {
			best, bestPriority = c.Value, p
		}
	}
	return best
}
