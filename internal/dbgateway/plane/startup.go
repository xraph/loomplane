package plane

import (
	"errors"
	"sort"
	"strings"
)

// startupParams turns the client's startup parameters into the session's
// initial overrides, as SQL text for the pool's SET replay, and the values to
// report to the client, both keyed by lowercased name. user, database and
// replication belong to the router and are ignored. options may carry
// `-c name=value` (or `--name=value`) pairs, read like the other parameters
// but applied first, as Postgres does; the router's `endpoint=` setting is
// skipped.
//
// client_encoding must be UTF8. Every other parameter must be one of
// KnownParams. The error message names a refused parameter but never echoes
// its value.
func startupParams(startup map[string]string) (overrides, shown map[string]string, err error) {
	var pairs [][2]string
	if opts, ok := startup["options"]; ok {
		if pairs, err = optionPairs(opts); err != nil {
			return nil, nil, err
		}
	}
	names := make([]string, 0, len(startup))
	for name := range startup {
		switch name {
		case "user", "database", "replication", "options":
		default:
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		pairs = append(pairs, [2]string{name, startup[name]})
	}

	overrides, shown = map[string]string{}, map[string]string{}
	for _, p := range pairs {
		name, value := p[0], p[1]
		key := strings.ToLower(name)
		switch {
		case key == "client_encoding":
			if !isUTF8(value) {
				return nil, nil, errors.New(msgEncoding)
			}
			continue
		case !isKnownParam(key):
			return nil, nil, unsupportedParam(name)
		case key == "search_path":
			// An identifier list, used verbatim, so only plain names pass.
			if !plainSearchPath(value) {
				return nil, nil, unsupportedParam(name)
			}
			overrides[key] = value
		default:
			overrides[key] = quoteLiteral(value)
		}
		shown[key] = value
	}
	return overrides, shown, nil
}

func unsupportedParam(name string) error {
	return errors.New("startup parameter " + name + " is not supported through the database gateway; set it with SET after connecting")
}

// optionPairs reads the name=value settings in a startup options string.
// Backslash escapes, and anything other than settings and the router's
// endpoint=, are refused.
func optionPairs(opts string) ([][2]string, error) {
	if strings.ContainsRune(opts, '\\') {
		return nil, unsupportedParam("options")
	}
	var pairs [][2]string
	fields := strings.Fields(opts)
	for i := 0; i < len(fields); i++ {
		tok := fields[i]
		switch {
		case tok == "-c":
			if i+1 == len(fields) {
				return nil, unsupportedParam("options")
			}
			i++
			tok = fields[i]
		case strings.HasPrefix(tok, "--"):
			tok = tok[2:]
		case strings.HasPrefix(tok, "-c"):
			tok = tok[2:]
		case strings.HasPrefix(tok, "endpoint="):
			continue
		default:
			return nil, unsupportedParam("options")
		}
		name, value, ok := strings.Cut(tok, "=")
		if !ok || name == "" {
			return nil, unsupportedParam("options")
		}
		// Postgres reads dashes in a setting's name as underscores.
		name = strings.ReplaceAll(name, "-", "_")
		if name == "endpoint" {
			continue
		}
		pairs = append(pairs, [2]string{name, value})
	}
	return pairs, nil
}

func isUTF8(v string) bool {
	return strings.EqualFold(v, "UTF8") || strings.EqualFold(v, "UTF-8") || strings.EqualFold(v, "UNICODE")
}

// plainSearchPath reports whether v matches ^[A-Za-z0-9_$, ]+$.
func plainSearchPath(v string) bool {
	if v == "" {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		plain := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c == ',' || c == ' '
		if !plain {
			return false
		}
	}
	return true
}

// quoteLiteral makes v a SQL string literal. A value holding a backslash
// becomes an escape string, E'...', which reads the same whatever
// standard_conforming_strings is on the connection it is replayed to.
func quoteLiteral(v string) string {
	if strings.ContainsRune(v, '\\') {
		return "E'" + strings.NewReplacer(`\`, `\\`, `'`, `''`).Replace(v) + "'"
	}
	return "'" + strings.ReplaceAll(v, "'", "''") + "'"
}
