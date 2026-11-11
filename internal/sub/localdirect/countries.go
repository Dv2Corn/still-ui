package localdirect

import "strings"

// ISO 3166-1 alpha-2, officially assigned codes only; the settings page keeps a copy
// that frontend/src/test/local-direct-countries.test.ts holds to this one.
const isoCountries = "ad ae af ag ai al am ao aq ar as at au aw ax az " +
	"ba bb bd be bf bg bh bi bj bl bm bn bo bq br bs bt bv bw by bz " +
	"ca cc cd cf cg ch ci ck cl cm cn co cr cu cv cw cx cy cz " +
	"de dj dk dm do dz ec ee eg eh er es et fi fj fk fm fo fr " +
	"ga gb gd ge gf gg gh gi gl gm gn gp gq gr gs gt gu gw gy " +
	"hk hm hn hr ht hu id ie il im in io iq ir is it je jm jo jp " +
	"ke kg kh ki km kn kp kr kw ky kz la lb lc li lk lr ls lt lu lv ly " +
	"ma mc md me mf mg mh mk ml mm mn mo mp mq mr ms mt mu mv mw mx my mz " +
	"na nc ne nf ng ni nl no np nr nu nz om " +
	"pa pe pf pg ph pk pl pm pn pr ps pt pw py qa re ro rs ru rw " +
	"sa sb sc sd se sg sh si sj sk sl sm sn so sr ss st sv sx sy sz " +
	"tc td tf tg th tj tk tl tm tn to tr tt tv tw tz ua ug um us uy uz " +
	"va vc ve vg vi vn vu wf ws ye yt za zm zw"

var knownCountries = func() map[string]struct{} {
	known := make(map[string]struct{}, 249)
	for code := range strings.FieldsSeq(isoCountries) {
		known[code] = struct{}{}
	}
	return known
}()

// Where a country's own domains differ from its ISO code: extra national TLDs,
// or a registry that never used the code (.uk).
var countryTLDs = map[string][]string{
	"ru": {"ru", "su", "xn--p1ai"},
	"by": {"by", "xn--90ais"},
	"kz": {"kz", "xn--80ao21a"},
	"gb": {"uk"},
}

// Curated geosite categories to try, best first; other countries try category-<code>.
var countryGeosites = map[string][]string{
	"cn": {"cn", "geolocation-cn"},
}
