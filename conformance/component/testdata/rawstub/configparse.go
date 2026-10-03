package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// parsed is a typed configuration value.
type parsed struct {
	set  bool
	s    string
	i    int64
	b    bool
	d    time.Duration
	ds   []time.Duration
	json any
}

type parseErr struct{ class, detail string }

// valueSpec is the input of one typed parse; it follows the operation parse_value of the
// be-protocol config vectors.
type valueSpec struct {
	format   string // string, int, bool, duration, durations, url, json, enum, locale, family_url
	value    *string
	def      *string
	required bool
	secret   bool
	minimum  *int64
	schemes  []string
	jsonKind string
	enum     []string
}

func parseValue(k keySpec, raw *string) (parsed, *parseErr) {
	p, err := parseTyped(valueSpec{format: k.format, value: raw, def: k.def, required: k.required, secret: k.secret, enum: k.enum})
	if err == nil && k.required && p.set && p.s == "" && (k.format == "string") {
		return parsed{}, &parseErr{"CONFIG_MISSING", "required key is empty"}
	}
	return p, err
}

// parseTyped applies the presence rules, then the format (vectors config, "Rules").
func parseTyped(v valueSpec) (parsed, *parseErr) {
	absent := v.value == nil || *v.value == "" // an empty string is unset (P2.3, vectors config)
	if absent {
		if v.def != nil {
			if *v.def == "" {
				return parsed{}, nil // an empty default means "not set" (OTEL_BASE_URL)
			}
			p, err := parsePresent(v, *v.def)
			if err != nil {
				return parsed{}, &parseErr{"CONFIG_INVALID", "default does not parse: " + err.detail}
			}
			return p, nil
		}
		if v.required {
			return parsed{}, &parseErr{"CONFIG_MISSING", "required key is missing"}
		}
		return parsed{}, nil
	}
	return parsePresent(v, *v.value)
}

var (
	intRe    = regexp.MustCompile(`^-?[0-9]+$`)
	localeRe = regexp.MustCompile(`^[A-Za-z]{2,8}(-[A-Za-z0-9]{1,8})*$`)
	hostRe   = regexp.MustCompile(`^(\[[0-9A-Fa-f:.]+\]|[A-Za-z0-9._~%-]+)(:([0-9]+))?$`)
	schemeRe = regexp.MustCompile(`^[a-z][a-z0-9+.-]*$`)
)

const maxSafeInt = 1<<53 - 1

func parsePresent(v valueSpec, s string) (parsed, *parseErr) {
	inv := func(d string) (parsed, *parseErr) { return parsed{}, &parseErr{"CONFIG_INVALID", d} }
	if v.secret {
		if !strings.HasPrefix(s, "/") || strings.HasSuffix(s, "/") {
			return inv("a secret key holds the absolute path of a file")
		}
		return parsed{set: true, s: s}, nil
	}
	switch v.format {
	case "string":
		return parsed{set: true, s: s}, nil
	case "int":
		if !intRe.MatchString(s) {
			return inv("not an integer")
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil || n > maxSafeInt || n < -maxSafeInt {
			return inv("integer out of range")
		}
		if v.minimum != nil && n < *v.minimum {
			return inv("integer below the minimum")
		}
		return parsed{set: true, i: n, s: s}, nil
	case "bool":
		switch s {
		case "true", "1":
			return parsed{set: true, b: true, s: s}, nil
		case "false", "0":
			return parsed{set: true, s: s}, nil
		}
		return inv("not a boolean (true, false, 1, 0)")
	case "duration":
		d, ok := parseDuration(s)
		if !ok {
			return inv("not a duration")
		}
		return parsed{set: true, d: d, s: s}, nil
	case "durations":
		var ds []time.Duration
		for _, part := range strings.Split(s, ",") {
			d, ok := parseDuration(part)
			if !ok || d <= 0 {
				return inv("not a list of positive durations")
			}
			ds = append(ds, d)
		}
		return parsed{set: true, ds: ds, s: s}, nil
	case "url":
		if !validURL(s, v.schemes) {
			return inv("not a URL scheme://host[:port][path]")
		}
		return parsed{set: true, s: s}, nil
	case "family_url":
		base, ok := familyURL(s)
		if !ok {
			return inv("not http://host:port")
		}
		return parsed{set: true, s: base}, nil
	case "json":
		j, ok := parseIJSON(s, v.jsonKind)
		if !ok {
			return inv("not I-JSON of the required kind")
		}
		return parsed{set: true, json: j, s: s}, nil
	case "zone":
		if s == "" || s == "Local" {
			return inv("not an IANA time zone")
		}
		if _, err := time.LoadLocation(s); err != nil {
			return inv("not an IANA time zone")
		}
		return parsed{set: true, s: s}, nil
	case "enum":
		for _, e := range v.enum {
			if s == e {
				return parsed{set: true, s: s}, nil
			}
		}
		return inv("not one of " + strings.Join(v.enum, ", "))
	case "locale":
		if !localeRe.MatchString(s) {
			return inv("not a BCP 47 tag")
		}
		return parsed{set: true, s: s}, nil
	}
	return inv("unknown format")
}

// parseDuration is Go syntax without negative values (vectors config).
func parseDuration(s string) (time.Duration, bool) {
	if s == "" || strings.ContainsAny(s, " \t\n") || strings.HasPrefix(s, "-") {
		return 0, false
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 {
		return 0, false
	}
	return d, true
}

func validURL(s string, schemes []string) bool {
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	i := strings.Index(s, "://")
	if i <= 0 || !schemeRe.MatchString(s[:i]) {
		return false
	}
	if len(schemes) > 0 && !contains(schemes, s[:i]) {
		return false
	}
	rest := s[i+3:]
	end := strings.IndexAny(rest, "/?#")
	host := rest
	if end >= 0 {
		host = rest[:end]
	}
	m := hostRe.FindStringSubmatch(host)
	if m == nil {
		return false
	}
	if m[3] != "" {
		p, err := strconv.Atoi(m[3])
		if err != nil || p < 1 || p > 65535 {
			return false
		}
	}
	return true
}

// familyURL validates a slot-family address (P2.10): http://host:port, explicit port, no path;
// one trailing slash is stripped.
func familyURL(s string) (string, bool) {
	s = strings.TrimSuffix(s, "/")
	if !strings.HasPrefix(s, "http://") || !validURL(s, nil) {
		return "", false
	}
	m := hostRe.FindStringSubmatch(s[len("http://"):])
	if m == nil || m[3] == "" {
		return "", false
	}
	return s, true
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// parseIJSON parses I-JSON: duplicate names and non-finite numbers are refused.
func parseIJSON(s, kind string) (any, bool) {
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	v, err := decodeStrict(dec)
	if err != nil {
		return nil, false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, false
	}
	switch kind {
	case "object":
		_, ok := v.(map[string]any)
		return v, ok
	case "array":
		_, ok := v.([]any)
		return v, ok
	}
	return v, true
}

func decodeStrict(dec *json.Decoder) (any, error) {
	t, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch tv := t.(type) {
	case json.Delim:
		if tv == '{' {
			m := map[string]any{}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k := kt.(string)
				if _, dup := m[k]; dup {
					return nil, errors.New("duplicate name")
				}
				if m[k], err = decodeStrict(dec); err != nil {
					return nil, err
				}
			}
			_, err := dec.Token()
			return m, err
		}
		var a []any
		for dec.More() {
			x, err := decodeStrict(dec)
			if err != nil {
				return nil, err
			}
			a = append(a, x)
		}
		_, err := dec.Token()
		if a == nil {
			a = []any{}
		}
		return a, err
	case json.Number:
		f, err := tv.Float64()
		if err != nil || math.IsInf(f, 0) || math.IsNaN(f) {
			return nil, errors.New("number out of range")
		}
		return f, nil
	}
	return t, nil
}

// jsonValid is a small helper for tests and handlers.
func jsonValid(b []byte) bool { return json.Valid(bytes.TrimSpace(b)) }
