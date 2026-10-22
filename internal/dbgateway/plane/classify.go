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
)

// Classification is what ClassifySQL found. Param and Value are set for
// EffectSet and EffectReset, and Channel for EffectListen and EffectUnlisten.
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
// It reads the first keyword for LISTEN, SET, RESET, DISCARD and COPY, and
// scans the whole text, minus string literals and comments, for advisory
// locks, temporary objects, WITH HOLD cursors, PREPARE and set_config. Text
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
	maskSQL(buf, sql)

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
		if w, _ := wordAt(buf, skipSpace(buf, p)); string(w) == "all" {
			return Classification{Effect: EffectDiscardAll}
		}
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
		prev = w
		i = j
	}
	if r.multi && r.hs >= 0 {
		r.startOfStatement(buf, r.hs, r.he)
	}
	return r
}

// startOfStatement handles the first word of one statement in text that holds
// several, where the first-keyword classification doesn't apply.
func (r *scan) startOfStatement(buf []byte, s, e int) {
	switch string(buf[s:e]) {
	case "listen", "unlisten", "set", "reset", "prepare", "do", "load":
		r.pin = true
	case "discard":
		if w, _ := wordAt(buf, skipSpace(buf, e)); string(w) == "all" {
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
	case "pg_advisory_lock", "pg_advisory_lock_shared", "pg_try_advisory_lock", "pg_try_advisory_lock_shared", "set_config":
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
		return Classification{}
	case "session":
		p = skipSpace(buf, e)
		w, e = wordAt(buf, p)
	}

	var param string
	if string(w) == "time" {
		// SET TIME ZONE v takes no TO or =.
		if z, ze := wordAt(buf, skipSpace(buf, e)); string(z) == "zone" {
			param, p = "timezone", ze
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
	if strings.EqualFold(value, "default") {
		return Classification{Effect: EffectReset, Param: param}
	}
	return Classification{Effect: EffectSet, Param: param, Value: value}
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

// maskSQL writes the lowercased code of src into dst, which is as long as src,
// so positions line up. Comments become spaces. String literals, with the
// quotes, become literalMark. A double-quoted identifier keeps its text, so
// that a call such as "pg_advisory_lock"(1) is seen, but a semicolon inside it
// becomes an underscore so that it can't read as a statement end.
func maskSQL(dst []byte, src string) {
	n := len(src)
	for i := 0; i < n; {
		c := src[i]
		switch {
		case c == '-' && i+1 < n && src[i+1] == '-':
			j := i + 2
			for j < n && src[j] != '\n' {
				j++
			}
			fill(dst[i:j], ' ')
			i = j
		case c == '/' && i+1 < n && src[i+1] == '*':
			j := blockCommentEnd(src, i)
			fill(dst[i:j], ' ')
			i = j
		case c == '\'':
			j := stringEnd(src, i)
			fill(dst[i:j], literalMark)
			i = j
		case c == '"':
			j := quotedIdentEnd(src, i)
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
// src[i]. A doubled quote is a quote in the string. After an E prefix a
// backslash escapes the next byte as well.
func stringEnd(src string, i int) int {
	escapes := i > 0 && src[i-1]|0x20 == 'e' && (i == 1 || !isWordByte(src[i-2]))
	j := i + 1
	for j < len(src) {
		switch {
		case escapes && src[j] == '\\':
			j += 2
		case src[j] == '\'':
			if j+1 < len(src) && src[j+1] == '\'' {
				j += 2
				continue
			}
			return j + 1
		default:
			j++
		}
	}
	return len(src)
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
