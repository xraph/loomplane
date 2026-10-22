package plane

import (
	"bytes"
	"strings"
)

// Effect says what a statement does to the client's session, and so whether
// the gateway can keep pooling the client's transactions.
type Effect int

const (
	// EffectNone leaves no session state behind. The statement can run on any
	// pooled backend connection.
	EffectNone Effect = iota
	// EffectPinSession creates session state the gateway cannot replay, so the
	// client keeps one backend connection until it disconnects.
	EffectPinSession
	// EffectPinStatement needs one backend connection only until the statement
	// completes (COPY streams data outside the normal request/response cycle).
	EffectPinStatement
	// EffectListen subscribes the session to Channel.
	EffectListen
	// EffectUnlisten drops the subscription to Channel, or all of them for "*".
	EffectUnlisten
	// EffectSet sets Param to Value for the session, and Param is one of
	// KnownParams, so the gateway can replay it on whichever backend runs the
	// next transaction.
	EffectSet
	// EffectReset returns Param to its default, or every parameter for "*".
	EffectReset
	// EffectDiscardAll is DISCARD ALL: it drops all session state.
	EffectDiscardAll
	// EffectDeallocate drops the SQL-level prepared statement named Param, or
	// all of them for "*" (DEALLOCATE ALL and DISCARD PLANS).
	EffectDeallocate
)

// Classification is what ClassifySQL found. Param and Value are set for
// EffectSet and EffectReset, Param alone for EffectDeallocate, and Channel for EffectListen and EffectUnlisten.
type Classification struct {
	Effect  Effect
	Param   string
	Value   string
	Channel string
}

// KnownParams are the session parameters the gateway tracks and replays
// instead of pinning. A SET of any other parameter pins the session.
var KnownParams = []string{
	"application_name", "search_path", "timezone", "statement_timeout", "lock_timeout",
	"idle_in_transaction_session_timeout", "datestyle", "intervalstyle", "extra_float_digits",
	"standard_conforming_strings", "default_transaction_isolation", "default_transaction_read_only", "work_mem",
}

// literalMark fills the place of a string literal in the masked text. It is
// neither whitespace nor part of a word, so a literal still counts as text
// after a semicolon.
const literalMark = 0x01

// ClassifySQL decides from the text of a simple Query or a Parse whether the
// statement creates session state. It errs toward pinning: anything that might
// create state the gateway can't replay is EffectPinSession.
//
// It reads the first keyword for LISTEN, SET, RESET, DISCARD, DEALLOCATE and
// COPY, and scans the whole text, minus string literals and comments, for
// advisory locks, temporary objects, WITH HOLD cursors, PREPARE, set_config
// and the other calls that change session state (see pinsAnywhere). Text
// holding more than one statement pins when any statement does, and LISTEN,
// UNLISTEN, SET and RESET always pin there, because the gateway can't tell
// which of the statements the client meant to track.
func ClassifySQL(sql string) Classification {
	// Short statements, the common case, are masked in a stack buffer.
	var stack [512]byte
	var buf []byte
	if len(sql) <= len(stack) {
		buf = stack[:len(sql)]
	} else {
		buf = make([]byte, len(sql))
	}
	m := maskSQL(buf, sql, false)
	if m.pin {
		return Classification{Effect: EffectPinSession}
	}
	c := classifyMasked(sql, buf)

	// A plain '...' string with a backslash lexes differently under
	// standard_conforming_strings = off, which a client may set, and the
	// gateway replays that setting on other backends. Read the text both ways
	// and pin if they disagree, so a quote the two readings see differently
	// can't hide a statement.
	if m.plainBackslash {
		alt := make([]byte, len(sql))
		maskSQL(alt, sql, true)
		if classifyMasked(sql, alt) != c {
			return Classification{Effect: EffectPinSession}
		}
	}
	return c
}

// classifyMasked classifies sql from its masked text.
func classifyMasked(sql string, buf []byte) Classification {
	r := scanWords(buf)
	switch {
	case r.pin:
		return Classification{Effect: EffectPinSession}
	case r.multi:
		if r.copyStmt {
			return Classification{Effect: EffectPinStatement}
		}
		return Classification{}
	case r.hs < 0:
		return Classification{}
	}

	p := r.he
	switch string(buf[r.hs:r.he]) {
	case "listen":
		return classifyChannel(sql, buf, p, EffectListen)
	case "unlisten":
		return classifyChannel(sql, buf, p, EffectUnlisten)
	case "set":
		return classifySet(sql, buf, p)
	case "reset":
		return classifyReset(sql, buf, p)
	case "discard":
		switch w, _ := wordAt(buf, skipSpace(buf, p)); string(w) {
		case "all":
			return Classification{Effect: EffectDiscardAll}
		case "plans":
			return Classification{Effect: EffectDeallocate, Param: "*"}
		}
	case "deallocate":
		return classifyDeallocate(sql, buf, p)
	case "copy":
		return Classification{Effect: EffectPinStatement}
	case "prepare", "do", "load":
		return Classification{Effect: EffectPinSession}
	}
	return Classification{}
}

// scan is what scanWords found in the masked text.
type scan struct {
	multi    bool // text follows a semicolon
	pin      bool // something in the text pins the session
	copyStmt bool // some statement is a COPY
	update   bool // the word update appears
	settings bool // the word pg_settings appears
	hs, he   int  // bounds of the first word of the first statement, hs is -1 if it isn't a word
}

// scanWords walks the masked text word by word. It reports the first word, whether
// more than one statement is present, and whether the text holds anything
// that pins the session wherever it appears.
func scanWords(buf []byte) scan {
	r := scan{hs: -1}
	var prev []byte
	stmt := 0
	atStart, sawSemi := true, false
	for i := 0; i < len(buf); {
		c := buf[i]
		if isSpace(c) {
			i++
			continue
		}
		if sawSemi {
			r.multi = true
		}
		if c == ';' {
			sawSemi, atStart = true, true
			stmt++
			i++
			continue
		}
		if !isWordByte(c) {
			atStart = false
			i++
			continue
		}
		j := i + 1
		for j < len(buf) && isWordByte(buf[j]) {
			j++
		}
		w := buf[i:j]
		if atStart {
			if stmt == 0 {
				r.hs, r.he = i, j
			} else {
				r.startOfStatement(buf, i, j)
			}
			atStart = false
		}
		if pinsAnywhere(w, prev) {
			r.pin = true
		}
		switch string(w) {
		case "update":
			r.update = true
		case "pg_settings":
			r.settings = true
		}
		prev = w
		i = j
	}
	if r.multi && r.hs >= 0 {
		r.startOfStatement(buf, r.hs, r.he)
	}
	// UPDATE pg_settings is SET by another name.
	if r.update && r.settings {
		r.pin = true
	}
	return r
}

// startOfStatement handles the first word of one statement in text that holds
// several, where the first-keyword classification doesn't apply.
func (r *scan) startOfStatement(buf []byte, s, e int) {
	switch string(buf[s:e]) {
	case "listen", "unlisten", "set", "reset", "prepare", "do", "load", "deallocate":
		r.pin = true
	case "discard":
		switch w, _ := wordAt(buf, skipSpace(buf, e)); string(w) {
		case "all", "plans":
			r.pin = true
		}
	case "copy":
		r.copyStmt = true
	}
}

// pinsAnywhere reports whether the word w, with the word before it, creates
// session state wherever it appears in the text. Unlock calls count: they mean
// a session lock exists. The xact advisory variants don't match, since they
// end with the transaction.
func pinsAnywhere(w, prev []byte) bool {
	switch string(w) {
	case "pg_advisory_lock", "pg_advisory_lock_shared", "pg_try_advisory_lock", "pg_try_advisory_lock_shared", "set_config",
		// setseed seeds the session's random() sequence. The query_to_xml
		// family and ts_stat run SQL held in a string, which is masked.
		// dblink_connect opens a named connection that lives as long as the
		// session.
		"setseed", "query_to_xml", "query_to_xmlschema", "query_to_xml_and_xmlschema", "ts_stat", "dblink_connect":
		return true
	case "temp", "temporary":
		switch string(prev) {
		case "create", "global", "local", "replace", "into":
			return true
		}
	case "hold":
		return string(prev) == "with"
	case "transaction":
		return string(prev) == "prepare"
	}
	// pg_temp is the schema of the session's temporary objects.
	return bytes.HasPrefix(w, []byte("pg_advisory_unlock")) || bytes.HasPrefix(w, []byte("pg_temp"))
}

// classifyChannel reads the channel of a LISTEN or UNLISTEN whose keyword ends at p.
func classifyChannel(sql string, buf []byte, p int, effect Effect) Classification {
	p = skipSpace(buf, p)
	var name string
	if p < len(buf) && buf[p] == '*' && effect == EffectUnlisten {
		name, p = "*", p+1
	} else {
		var ok bool
		if name, p, ok = identAt(sql, buf, p); !ok {
			return Classification{Effect: EffectPinSession}
		}
	}
	if !atEnd(buf, p) {
		return Classification{Effect: EffectPinSession}
	}
	return Classification{Effect: effect, Channel: name}
}

// classifyReset reads the parameter of a RESET whose keyword ends at p.
func classifyReset(sql string, buf []byte, p int) Classification {
	p = skipSpace(buf, p)
	if w, e := wordAt(buf, p); string(w) == "all" {
		if atEnd(buf, e) {
			return Classification{Effect: EffectReset, Param: "*"}
		}
		return Classification{Effect: EffectPinSession}
	}
	name, p, ok := identAt(sql, buf, p)
	if !ok || !atEnd(buf, p) {
		return Classification{Effect: EffectPinSession}
	}
	name = strings.ToLower(name)
	if !isKnownParam(name) {
		return Classification{Effect: EffectPinSession}
	}
	return Classification{Effect: EffectReset, Param: name}
}

// classifySet reads a SET whose keyword ends at p. SET LOCAL and SET
// TRANSACTION end with the transaction, so they leave nothing behind.
func classifySet(sql string, buf []byte, p int) Classification {
	pin := Classification{Effect: EffectPinSession}
	p = skipSpace(buf, p)
	w, e := wordAt(buf, p)
	switch string(w) {
	case "local", "transaction":
		// SET local.foo is a custom parameter set for the session.
		if e >= len(buf) || buf[e] != '.' {
			return Classification{}
		}
	case "session":
		p = skipSpace(buf, e)
		w, e = wordAt(buf, p)
	}

	var param string
	timeZone := false
	if string(w) == "time" {
		// SET TIME ZONE v takes no TO or =.
		if z, ze := wordAt(buf, skipSpace(buf, e)); string(z) == "zone" {
			param, p = "timezone", ze
			timeZone = true
		}
	}
	if param == "" {
		name, end, ok := identAt(sql, buf, p)
		if !ok {
			return pin
		}
		param, p = strings.ToLower(name), skipSpace(buf, end)
		switch {
		case p < len(buf) && buf[p] == '=':
			p++
		default:
			to, te := wordAt(buf, p)
			if string(to) != "to" {
				return pin
			}
			p = te
		}
	}

	// The value is everything up to the end of the statement, minus a trailing
	// semicolon and comments. Literals are masked but not blank, so a quoted
	// value is never trimmed away.
	start := skipSpace(buf, p)
	end := len(buf)
	for end > start && isSpace(buf[end-1]) {
		end--
	}
	if end > start && buf[end-1] == ';' {
		end--
		for end > start && isSpace(buf[end-1]) {
			end--
		}
	}
	if end <= start || !isKnownParam(param) {
		return pin
	}
	value := sql[start:end]
	if strings.EqualFold(value, "default") || timeZone && strings.EqualFold(value, "local") {
		return Classification{Effect: EffectReset, Param: param}
	}
	if timeZone {
		// SET TIME ZONE INTERVAL '+00:00' HOUR TO MINUTE has a syntax of its own.
		if w, _ := wordAt(buf, start); string(w) == "interval" {
			return pin
		}
	}
	return Classification{Effect: EffectSet, Param: param, Value: value}
}

// classifyDeallocate reads a DEALLOCATE whose keyword ends at p:
// DEALLOCATE [PREPARE] name, or ALL.
func classifyDeallocate(sql string, buf []byte, p int) Classification {
	pin := Classification{Effect: EffectPinSession}
	p = skipSpace(buf, p)
	w, e := wordAt(buf, p)
	if string(w) == "prepare" && !atEnd(buf, e) {
		// PREPARE is optional, and also a legal statement name.
		p = skipSpace(buf, e)
		w, e = wordAt(buf, p)
	}
	if string(w) == "all" {
		if atEnd(buf, e) {
			return Classification{Effect: EffectDeallocate, Param: "*"}
		}
		return pin
	}
	name, end, ok := identAt(sql, buf, p)
	if !ok || !atEnd(buf, end) {
		return pin
	}
	return Classification{Effect: EffectDeallocate, Param: name}
}

func isKnownParam(name string) bool {
	for _, k := range KnownParams {
		if k == name {
			return true
		}
	}
	return false
}

// identAt reads an identifier at p. An unquoted one is lowercased, as Postgres
// folds it. A double-quoted one keeps its case and loses its quotes, so it is
// read from sql, since the masked text has folded it.
func identAt(sql string, buf []byte, p int) (name string, end int, ok bool) {
	if p >= len(buf) {
		return "", p, false
	}
	if buf[p] != '"' {
		w, e := wordAt(buf, p)
		return string(w), e, len(w) > 0
	}
	e := quotedIdentEnd(sql, p)
	if e-p < 3 || sql[e-1] != '"' {
		return "", p, false
	}
	return strings.ReplaceAll(sql[p+1:e-1], `""`, `"`), e, true
}

// wordAt returns the word that starts at p, and where it ends.
func wordAt(buf []byte, p int) ([]byte, int) {
	e := p
	for e < len(buf) && isWordByte(buf[e]) {
		e++
	}
	return buf[p:e], e
}

func skipSpace(buf []byte, p int) int {
	for p < len(buf) && isSpace(buf[p]) {
		p++
	}
	return p
}

// atEnd reports whether only an optional semicolon follows p.
func atEnd(buf []byte, p int) bool {
	p = skipSpace(buf, p)
	if p < len(buf) && buf[p] == ';' {
		p = skipSpace(buf, p+1)
	}
	return p == len(buf)
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// isWordByte reports whether c can be part of an unquoted identifier or keyword.
// Bytes above 0x7f count, as Postgres treats them as letters.
func isWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '$' || c >= 0x80
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// masked is what maskSQL noticed besides the masked text.
type masked struct {
	pin            bool // text the classifier can't read, such as a decoded U& identifier
	plainBackslash bool // a plain '...' string holds a backslash
}

// maskSQL writes the lowercased code of src into dst, which is as long as src,
// so positions line up. Comments become spaces. String literals, with the
// quotes, become literalMark. A double-quoted identifier keeps its text, so
// that a call such as "pg_advisory_lock"(1) is seen, but a semicolon inside it
// becomes an underscore so that it can't read as a statement end. With
// allEscapes, a backslash escapes in every string, as it does under
// standard_conforming_strings = off.
func maskSQL(dst []byte, src string, allEscapes bool) masked {
	var m masked
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '-' && i+1 < n && src[i+1] == '-':
			j := i + 2
			for j < n && src[j] != '\n' && src[j] != '\r' {
				j++
			}
			fill(dst[i:j], ' ')
			i = j
		case c == '/' && i+1 < n && src[i+1] == '*':
			j := blockCommentEnd(src, i)
			fill(dst[i:j], ' ')
			i = j
		case c == '\'':
			j, plainBackslash := stringEnd(src, i, allEscapes)
			if plainBackslash {
				m.plainBackslash = true
			}
			fill(dst[i:j], literalMark)
			i = j
		case c == '"':
			j := quotedIdentEnd(src, i)
			if isUnicodeIdent(src, i) && (strings.IndexByte(src[i:j], '\\') >= 0 || uescapeFollows(src, j)) {
				// U&"..." names are decoded from escapes, and UESCAPE changes the escape character.
				m.pin = true
			}
			for k := i; k < j; k++ {
				dst[k] = lowerByte(src[k])
				if dst[k] == ';' {
					dst[k] = '_'
				}
			}
			i = j
		case c == '$':
			if j := dollarStringEnd(src, i); j > i {
				fill(dst[i:j], literalMark)
				i = j
				continue
			}
			dst[i] = c
			i++
		default:
			dst[i] = lowerByte(c)
			i++
		}
	}
	return m
}

// isUnicodeIdent reports whether the double quote at src[i] opens a U&"..." identifier.
func isUnicodeIdent(src string, i int) bool {
	return i >= 2 && src[i-1] == '&' && src[i-2]|0x20 == 'u' && (i == 2 || !isWordByte(src[i-3]))
}

// uescapeFollows reports whether the keyword UESCAPE comes next after p.
func uescapeFollows(src string, p int) bool {
	for p < len(src) {
		switch {
		case isSpace(src[p]):
			p++
		case src[p] == '-' && p+1 < len(src) && src[p+1] == '-':
			for p < len(src) && src[p] != '\n' && src[p] != '\r' {
				p++
			}
		case src[p] == '/' && p+1 < len(src) && src[p+1] == '*':
			p = blockCommentEnd(src, p)
		default:
			const kw = "uescape"
			if len(src)-p < len(kw) || !strings.EqualFold(src[p:p+len(kw)], kw) {
				return false
			}
			return p+len(kw) == len(src) || !isWordByte(src[p+len(kw)])
		}
	}
	return false
}

func fill(b []byte, c byte) {
	for i := range b {
		b[i] = c
	}
}

// blockCommentEnd returns the index after the block comment that starts at
// src[i]. Block comments nest, as in Postgres. An unterminated one runs to the
// end of the text.
func blockCommentEnd(src string, i int) int {
	depth := 1
	j := i + 2
	for j < len(src) {
		switch {
		case src[j] == '/' && j+1 < len(src) && src[j+1] == '*':
			depth++
			j += 2
		case src[j] == '*' && j+1 < len(src) && src[j+1] == '/':
			depth--
			j += 2
			if depth == 0 {
				return j
			}
		default:
			j++
		}
	}
	return len(src)
}

// stringEnd returns the index after the single-quoted string that starts at
// src[i]. A doubled quote is a quote in the string. After an E prefix, or with
// allEscapes, a backslash escapes the next byte as well. A string that is
// followed by a newline and another quoted string is one string, read in the
// same mode. plainBackslash reports a backslash in a string that was read
// without escapes.
func stringEnd(src string, i int, allEscapes bool) (end int, plainBackslash bool) {
	escapes := allEscapes || i > 0 && src[i-1]|0x20 == 'e' && (i == 1 || !isWordByte(src[i-2]))
	end = len(src)
	for j := i + 1; j < len(src); {
		switch {
		case escapes && src[j] == '\\':
			j += 2
		case src[j] == '\'':
			if j+1 < len(src) && src[j+1] == '\'' {
				j += 2
				continue
			}
			if k, ok := stringContinuation(src, j+1); ok {
				j = k + 1
				continue
			}
			end = j + 1
			j = len(src)
		default:
			j++
		}
	}
	return end, !escapes && strings.IndexByte(src[i:end], '\\') >= 0
}

// stringContinuation reports whether the string that closed just before
// src[p] goes on, and if so where its next quote is. Postgres joins two
// strings that are separated only by whitespace holding a newline, where a
// line comment counts as whitespace.
func stringContinuation(src string, p int) (int, bool) {
	n := len(src)
	// Whitespace and comments on the line of the closing quote.
	for p < n {
		if src[p] == ' ' || src[p] == '\t' || src[p] == '\f' {
			p++
		} else if src[p] == '-' && p+1 < n && src[p+1] == '-' {
			p += 2
			for p < n && src[p] != '\n' && src[p] != '\r' {
				p++
			}
		} else {
			break
		}
	}
	if p >= n || src[p] != '\n' && src[p] != '\r' {
		return 0, false
	}
	p++
	// More whitespace, and comments that end with a newline.
	for p < n {
		c := src[p]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' {
			p++
		} else if c == '-' && p+1 < n && src[p+1] == '-' {
			p += 2
			for p < n && src[p] != '\n' && src[p] != '\r' {
				p++
			}
			if p >= n {
				return 0, false
			}
			p++
		} else {
			break
		}
	}
	if p < n && src[p] == '\'' {
		return p, true
	}
	return 0, false
}

// quotedIdentEnd returns the index after the double-quoted identifier that
// starts at src[i].
func quotedIdentEnd(src string, i int) int {
	j := i + 1
	for j < len(src) {
		if src[j] == '"' {
			if j+1 < len(src) && src[j+1] == '"' {
				j += 2
				continue
			}
			return j + 1
		}
		j++
	}
	return len(src)
}

// dollarStringEnd returns the index after the dollar-quoted string, $$...$$ or
// $tag$...$tag$, that starts at src[i], or i if a dollar sign there starts
// something else (a parameter such as $1, or the middle of an identifier).
func dollarStringEnd(src string, i int) int {
	if i > 0 && isWordByte(src[i-1]) {
		return i
	}
	j := i + 1
	if j < len(src) && (src[j] >= 'a' && src[j] <= 'z' || src[j] >= 'A' && src[j] <= 'Z' || src[j] == '_' || src[j] >= 0x80) {
		for j < len(src) && src[j] != '$' && isWordByte(src[j]) {
			j++
		}
	}
	if j >= len(src) || src[j] != '$' {
		return i
	}
	delim := src[i : j+1]
	k := strings.Index(src[j+1:], delim)
	if k < 0 {
		return len(src)
	}
	return j + 1 + k + len(delim)
}
