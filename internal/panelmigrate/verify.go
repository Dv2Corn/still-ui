package panelmigrate

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"
)

// VerifiedSettings must read the same after the move: they decide where the
// panel and the subscription server listen and with which certificate, so a
// change would break bookmarks, firewall rules and every subscription URL.
var VerifiedSettings = []string{
	"webListen", "webDomain", "webPort", "webCertFile", "webKeyFile", "webBasePath",
	"subEnable", "subListen", "subPort", "subPath", "subDomain", "subCertFile", "subKeyFile",
	"subEncrypt", "subURI", "twoFactorEnable",
}

// Verify compares the report taken from the 3x-ui database before the move
// with the one taken from still-ui's database after it. It returns one line
// per check; failed checks start with "FAIL".
func Verify(pre, post *Report) (lines []string, ok bool) {
	ok = true
	fail := func(format string, a ...any) {
		ok = false
		lines = append(lines, "FAIL "+fmt.Sprintf(format, a...))
	}
	pass := func(format string, a ...any) { lines = append(lines, "ok   "+fmt.Sprintf(format, a...)) }

	if pre.Inbounds != post.Inbounds {
		fail("inbounds: %d before, %d after", pre.Inbounds, post.Inbounds)
	} else {
		pass("inbounds: %d", post.Inbounds)
	}
	after := map[int]InboundBrief{}
	for _, in := range post.InboundList {
		after[in.ID] = in
	}
	for _, in := range pre.InboundList {
		got, found := after[in.ID]
		switch {
		case !found:
			fail("inbound %d (port %d) is missing", in.ID, in.Port)
		case got.Port != in.Port:
			fail("inbound %d: port %d before, %d after", in.ID, in.Port, got.Port)
		case got.Enable != in.Enable:
			fail("inbound %d: enable %v before, %v after", in.ID, in.Enable, got.Enable)
		case !sameProtocol(in.Protocol, got.Protocol):
			fail("inbound %d: protocol %s before, %s after", in.ID, in.Protocol, got.Protocol)
		}
	}

	have := map[string]bool{}
	for _, e := range post.ClientEmails {
		have[e] = true
	}
	var missing []string
	for _, e := range pre.ClientEmails {
		if !have[e] {
			missing = append(missing, e)
		}
	}
	switch {
	case len(missing) > 0:
		fail("clients: %d before, %d after; missing: %s", pre.Clients, post.Clients, strings.Join(firstN(missing, 10), ", "))
	case pre.Clients != post.Clients:
		fail("clients: %d before, %d after", pre.Clients, post.Clients)
	default:
		pass("clients: %d", post.Clients)
	}

	if len(pre.Logins) != len(post.Logins) {
		fail("panel accounts: %d before, %d after", len(pre.Logins), len(post.Logins))
	} else {
		loginOK := true
		for i := range pre.Logins {
			a, b := pre.Logins[i], post.Logins[i]
			if a.Username != b.Username {
				fail("panel account %d: username changed", i+1)
				loginOK = false
			} else if a.Bcrypt && a.PasswordSHA256 != b.PasswordSHA256 {
				fail("panel account %q: password hash changed", a.Username)
				loginOK = false
			} else if !b.Bcrypt {
				fail("panel account %q: password is not stored hashed", a.Username)
				loginOK = false
			}
		}
		if loginOK {
			pass("panel login unchanged (%d account(s))", len(post.Logins))
		}
	}
	if pre.TwoFactorSHA256 != post.TwoFactorSHA256 {
		fail("two-factor secret changed")
	}

	settingsOK := true
	for _, k := range VerifiedSettings {
		a, aok := pre.Settings[k]
		b, bok := post.Settings[k]
		if aok != bok || a != b {
			fail("setting %s: %q before, %q after", k, a, b)
			settingsOK = false
		}
	}
	if settingsOK {
		pass("panel and subscription settings unchanged")
	}
	return lines, ok
}

// sameProtocol tolerates the renames still-ui's own migrations perform.
func sameProtocol(a, b string) bool {
	a, b = strings.ToLower(a), strings.ToLower(b)
	if a == b {
		return true
	}
	// Legacy SOCKS inbounds are migrated to the "mixed" protocol.
	return a == "socks" && b == "mixed"
}

func firstN(s []string, n int) []string {
	if len(s) > n {
		return append(s[:n:n], fmt.Sprintf("and %d more", len(s)-n))
	}
	return s
}

// WriteEnv prints the scalar parts of r as "key<TAB>value" lines, for a shell
// script to read with `while IFS=$'\t' read -r key value` — no eval needed.
// Values with a tab or line break are left out: none of the settings below
// can legitimately hold one.
func WriteEnv(w io.Writer, r *Report) error {
	emit := func(k, v string) error {
		if strings.ContainsAny(v, "\t\r\n") {
			return nil
		}
		_, err := fmt.Fprintf(w, "%s\t%s\n", k, v)
		return err
	}
	if err := emit("dialect", r.Dialect); err != nil {
		return err
	}
	if err := emit("inbounds", fmt.Sprint(r.Inbounds)); err != nil {
		return err
	}
	if err := emit("clients", fmt.Sprint(r.Clients)); err != nil {
		return err
	}
	for _, k := range SettingKeys {
		if v, ok := r.Settings[k]; ok {
			if err := emit("setting."+k, v); err != nil {
				return err
			}
		}
	}
	for _, id := range r.SampleSubIDs {
		if err := emit("sampleSubId", id); err != nil {
			return err
		}
	}
	return nil
}

// Subscription comparison results.
const (
	SubIdentical     = 0 // byte for byte
	SubDifferent     = 1
	SubRemarksDiffer = 3 // the same links; only remark text differs
	// SubSameSettings: every link carries every parameter it had before with
	// the same value; the new one only adds parameters (a newer link format,
	// e.g. encryption=none) and the spiderX path 2.x randomised per request.
	SubSameSettings = 4
)

// randomPerRequest are link parameters 3x-ui 2.x drew at random for every
// subscription request; their value says nothing about the client's settings.
var randomPerRequest = map[string]bool{"spx": true}

// CompareSubscriptions compares two subscription bodies, before (a) and after
// (b) the move. Identical bytes are the expected outcome when 3x-ui and
// still-ui share the link generator. Otherwise both are decoded (base64
// bodies, vmess:// JSON) and compared link by link without remarks: with
// "show usage info" on a remark carries traffic figures that move on their own
// while the migration runs. Last, older 3x-ui releases wrote fewer link
// parameters: the links still match when every parameter of a link in a also
// appears, with the same value, in the link at the same place in b. The
// returned note names what b adds.
func CompareSubscriptions(a, b []byte) (int, string) {
	if bytes.Equal(a, b) {
		return SubIdentical, ""
	}
	la, lb := subscriptionLinks(a), subscriptionLinks(b)
	if len(la) == 0 || len(la) != len(lb) {
		return SubDifferent, fmt.Sprintf("%d link(s) before, %d after", len(la), len(lb))
	}
	same := true
	for i := range la {
		if linkWithoutRemark(la[i]) != linkWithoutRemark(lb[i]) {
			same = false
			break
		}
	}
	if same {
		return SubRemarksDiffer, ""
	}
	added := map[string]bool{}
	for i := range la {
		ok, extra, why := sameLinkSettings(la[i], lb[i])
		if !ok {
			return SubDifferent, fmt.Sprintf("link %d: %s", i+1, why)
		}
		for _, k := range extra {
			added[k] = true
		}
	}
	names := make([]string, 0, len(added))
	for k := range added {
		names = append(names, k)
	}
	sort.Strings(names)
	note := ""
	if len(names) > 0 {
		note = "added: " + strings.Join(names, ", ")
	}
	return SubSameSettings, note
}

// subscriptionLinks returns the links of a raw subscription body.
func subscriptionLinks(body []byte) []string {
	text := strings.TrimSpace(string(body))
	if !strings.Contains(text, "://") {
		if dec, ok := decodeBase64(text); ok && strings.Contains(dec, "://") {
			text = dec
		}
	}
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return out
}

func linkWithoutRemark(link string) string {
	if obj, ok := vmessObject(link); ok {
		delete(obj, "ps")
		if canon, err := json.Marshal(obj); err == nil { // map keys come out sorted
			return "vmess://" + string(canon)
		}
		return link
	}
	if i := strings.IndexByte(link, '#'); i >= 0 {
		return link[:i]
	}
	return link
}

func vmessObject(link string) (map[string]any, bool) {
	payload, ok := strings.CutPrefix(link, "vmess://")
	if !ok {
		return nil, false
	}
	dec, ok := decodeBase64(payload)
	if !ok {
		return nil, false
	}
	var obj map[string]any
	if json.Unmarshal([]byte(dec), &obj) != nil {
		return nil, false
	}
	return obj, true
}

// linkFields flattens a link into comparable fields: scheme, userinfo, host,
// port, path and every query parameter (vmess: every JSON field). The remark
// (fragment, vmess "ps") is not a field.
func linkFields(link string) (map[string]string, bool) {
	fields := map[string]string{}
	if obj, ok := vmessObject(link); ok {
		fields[" scheme"] = "vmess"
		for k, v := range obj {
			if k != "ps" {
				fields[k] = fmt.Sprint(v)
			}
		}
		return fields, true
	}
	u, err := url.Parse(link)
	if err != nil || u.Scheme == "" {
		return nil, false
	}
	fields[" scheme"] = u.Scheme
	fields[" user"] = u.User.String()
	if u.Scheme == "ss" {
		fields[" user"] = shadowsocksUserinfo(link)
	}
	fields[" host"] = u.Hostname()
	fields[" port"] = u.Port()
	fields[" path"] = u.Path
	if u.Opaque != "" {
		fields[" opaque"] = u.Opaque
	}
	for k, vs := range u.Query() {
		fields[k] = strings.Join(vs, ",")
	}
	return fields, true
}

// sameLinkSettings reports whether link b keeps every setting of link a and
// lists the parameters b adds. Reasons name fields, never their values (they
// include client secrets).
func sameLinkSettings(a, b string) (bool, []string, string) {
	fa, okA := linkFields(a)
	fb, okB := linkFields(b)
	if !okA || !okB {
		return false, nil, "not a share link"
	}
	for k, va := range fa {
		vb, has := fb[k]
		switch {
		case randomPerRequest[k] && has:
		case !has:
			return false, nil, fmt.Sprintf("%q is missing", strings.TrimSpace(k))
		case va != vb:
			return false, nil, fmt.Sprintf("%q changed", strings.TrimSpace(k))
		}
	}
	var extra []string
	for k := range fb {
		if _, had := fa[k]; !had {
			extra = append(extra, k)
		}
	}
	return true, extra, ""
}

// shadowsocksUserinfo returns "method:password[:password]" of an ss:// link
// in either SIP002 spelling: base64 of it (3x-ui 2.x, and every non-2022
// cipher) or, for the 2022 ciphers, the percent-encoded plain text that newer
// releases write.
func shadowsocksUserinfo(link string) string {
	rest := strings.TrimPrefix(link, "ss://")
	at := strings.LastIndexByte(rest, '@')
	if at < 0 {
		return ""
	}
	raw := rest[:at]
	if dec, ok := decodeBase64(raw); ok && strings.Contains(dec, ":") && utf8.ValidString(dec) {
		return dec
	}
	if dec, err := url.QueryUnescape(raw); err == nil {
		return dec
	}
	return raw
}

func decodeBase64(s string) (string, bool) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), true
		}
	}
	return "", false
}
