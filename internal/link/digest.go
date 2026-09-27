package link

import (
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

// challenge is a parsed "WWW-Authenticate: Digest ..." header (RFC 7616,
// MD5 only, which is what PrusaLink uses).
type challenge struct {
	realm, nonce, opaque, algorithm string
	qopAuth                         bool
	stale                           bool
}

func parseChallenge(header string) (*challenge, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSpace(header), "Digest ")
	if !ok {
		return nil, false
	}
	params := map[string]string{}
	for _, part := range splitParams(rest) {
		k, v, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		params[strings.ToLower(strings.TrimSpace(k))] = strings.Trim(strings.TrimSpace(v), `"`)
	}
	if params["nonce"] == "" {
		return nil, false
	}
	c := &challenge{
		realm:     params["realm"],
		nonce:     params["nonce"],
		opaque:    params["opaque"],
		algorithm: params["algorithm"],
		stale:     strings.EqualFold(params["stale"], "true"),
	}
	for _, q := range strings.Split(params["qop"], ",") {
		if strings.TrimSpace(q) == "auth" {
			c.qopAuth = true
		}
	}
	return c, true
}

// splitParams splits on commas outside quotes.
func splitParams(s string) []string {
	var out []string
	var cur strings.Builder
	quoted := false
	for _, r := range s {
		switch {
		case r == '"':
			quoted = !quoted
			cur.WriteRune(r)
		case r == ',' && !quoted:
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

func md5hex(s string) string {
	sum := md5.Sum([]byte(s))
	return hex.EncodeToString(sum[:])
}

// authorization builds the Authorization header for one request.
func (c *challenge) authorization(user, password, method, uri string, nc uint32) string {
	ha1 := md5hex(user + ":" + c.realm + ":" + password)
	ha2 := md5hex(method + ":" + uri)
	var b strings.Builder
	fmt.Fprintf(&b, `Digest username="%s", realm="%s", nonce="%s", uri="%s"`, user, c.realm, c.nonce, uri)
	if c.qopAuth {
		cb := make([]byte, 8)
		_, _ = rand.Read(cb)
		cnonce := hex.EncodeToString(cb)
		ncs := fmt.Sprintf("%08x", nc)
		resp := md5hex(strings.Join([]string{ha1, c.nonce, ncs, cnonce, "auth", ha2}, ":"))
		fmt.Fprintf(&b, `, qop=auth, nc=%s, cnonce="%s", response="%s"`, ncs, cnonce, resp)
	} else {
		fmt.Fprintf(&b, `, response="%s"`, md5hex(ha1+":"+c.nonce+":"+ha2))
	}
	if c.opaque != "" {
		fmt.Fprintf(&b, `, opaque="%s"`, c.opaque)
	}
	if c.algorithm != "" {
		fmt.Fprintf(&b, `, algorithm=%s`, c.algorithm)
	}
	return b.String()
}
