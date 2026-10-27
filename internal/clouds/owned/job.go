package owned

import (
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"
)

// JobOutputLimit is how much of a job's output its result keeps.
const JobOutputLimit = 16 << 10

// jobRedactRun is the shortest run of a secret's bytes JobOutput hides
// wherever it appears. Shorter runs are too common in ordinary output to
// hide, and too short to give a secret away.
const jobRedactRun = 4

// JobOutput is the end of what a job wrote, at most JobOutputLimit bytes,
// with secrets hidden. It doesn't depend on where the log it's given
// starts or ends, or on how a target interleaved stdout and stderr, so a
// secret cut or split anywhere is still hidden:
//   - Invalid UTF-8 is dropped first, so it can't split a secret.
//   - Each whole secret value becomes "[redacted]", the longest first.
//   - Then every run of output covered by four-byte pieces of a secret's
//     sensitive part becomes "[redacted]": at most three bytes of one in a
//     row can be left, at a cut or between other output.
//   - Only then is it cut to its last JobOutputLimit bytes, which can't
//     leave anything new of a secret.
func JobOutput(output []byte, secrets map[string]string) string {
	text := strings.ToValidUTF8(string(output), "")
	var values []string
	pieces := map[string]bool{}
	for _, value := range secrets {
		if value = strings.ToValidUTF8(value, ""); value == "" {
			continue
		}
		values = append(values, value)
		for _, piece := range sensitivePieces(value) {
			pieces[piece] = true
		}
	}
	slices.SortFunc(values, func(a, b string) int { return len(b) - len(a) })
	for _, value := range values {
		text = strings.ReplaceAll(text, value, "[redacted]")
	}
	text = redactPieces(text, pieces)
	if len(text) > JobOutputLimit {
		text = text[len(text)-JobOutputLimit:]
		for text != "" && !utf8.RuneStart(text[0]) {
			text = text[1:]
		}
	}
	return text
}

// sensitiveParts is what of a secret value must never be shown. Of an
// address (a URL with a scheme and a host) with a password it's what's
// secret in it: its password, its user when that looks like a token
// (urlTokenUser), and its query values that look secret
// (secretQueryValue), and its path and fragment, each as decoded and as
// the address spells it. Its scheme, host and an ordinary user, such as
// postgres, are left, so output naming them stays readable. An address
// without a password is secret whole, since its path can hold the secret
// (a bot's token, a webhook's key), and its token user and secret query
// values count on their own too. Anything else is secret whole. A value
// of several lines is covered line by line too, since its pieces include
// each line's.
func sensitiveParts(value string) []string {
	u, err := url.Parse(value)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return []string{value}
	}
	var parts []string
	if password, ok := u.User.Password(); !ok || password == "" {
		parts = append(parts, value)
	}
	add := func(decoded, raw string) {
		for _, part := range []string{decoded, raw} {
			if part != "" && !slices.Contains(parts, part) {
				parts = append(parts, part)
			}
		}
	}
	if u.User != nil {
		// As the address spells them, which may escape them.
		rawUser, rawPassword := "", ""
		if _, rest, found := strings.Cut(value, "://"); found {
			if at := strings.LastIndex(rest, "@"); at >= 0 {
				rawUser, rawPassword, _ = strings.Cut(rest[:at], ":")
			}
		}
		if password, ok := u.User.Password(); ok && password != "" {
			add(password, rawPassword)
			// Its path and fragment can hold a token too, as a
			// webhook's does. Its scheme and host stay readable.
			add(u.Path, u.EscapedPath())
			add(u.Fragment, u.EscapedFragment())
		}
		if user := u.User.Username(); len(user) >= urlTokenUser {
			add(user, rawUser)
		}
	}
	for _, pair := range strings.Split(u.RawQuery, "&") {
		rawKey, rawValue, _ := strings.Cut(pair, "=")
		key, kerr := url.QueryUnescape(rawKey)
		v, verr := url.QueryUnescape(rawValue)
		if kerr != nil || verr != nil {
			key, v = rawKey, rawValue
		}
		if secretQueryValue(key, v) {
			add(v, rawValue)
		}
	}
	return parts
}

// sensitivePieces are the jobRedactRun-byte pieces of value's sensitive
// parts. A piece of an address's path or fragment that its scheme and host
// spell too isn't one, so output naming them stays readable: a database
// named app__postgres doesn't hide "postgres" in postgresql://. Those
// pieces are public in the address already.
func sensitivePieces(value string) []string {
	var public string
	located := map[string]bool{}
	if u, err := url.Parse(value); err == nil && u.Scheme != "" && u.Host != "" {
		if password, ok := u.User.Password(); ok && password != "" {
			public = strings.ToLower(u.Scheme + "://" + u.Host)
			for _, part := range []string{u.Path, u.EscapedPath(), u.Fragment, u.EscapedFragment()} {
				located[part] = true
			}
			for _, part := range []string{password, u.User.Username()} {
				delete(located, part)
			}
		}
	}
	var pieces []string
	for _, part := range sensitiveParts(value) {
		for i := 0; i+jobRedactRun <= len(part); i++ {
			piece := part[i : i+jobRedactRun]
			if located[part] && strings.Contains(public, strings.ToLower(piece)) {
				continue
			}
			pieces = append(pieces, piece)
		}
	}
	return pieces
}

// urlTokenUser is the shortest user of an address that is a secret
// itself, such as a token a Git host takes as the user. Shorter ones are
// names, such as postgres.
const urlTokenUser = 16

// secretQueryWords are words of a query parameter's name that make its
// value a secret, such as api_key, access-token or X-Amz-Signature.
var secretQueryWords = map[string]bool{
	"token": true, "key": true, "secret": true, "password": true, "passwd": true, "pwd": true, "pass": true,
	"auth": true, "signature": true, "sig": true, "credential": true, "credentials": true,
}

// secretQueryNames are whole parameter names, lower case without
// separators, that are secrets though no word of them says so alone.
var secretQueryNames = map[string]bool{
	"apikey": true, "accesstoken": true, "authtoken": true, "refreshtoken": true, "idtoken": true, "sessiontoken": true,
	"clientsecret": true, "secretkey": true, "privatekey": true, "accesskey": true, "sslpassword": true,
}

// plainQueryNames are parameters whose values aren't secrets though a
// word of their name says otherwise: file paths and settings.
var plainQueryNames = map[string]bool{
	"sslkey": true, "sslcert": true, "sslrootcert": true, "sslcrl": true,
	"authsource": true, "authmechanism": true, "authmechanismproperties": true,
}

// secretQueryValue reports whether a query value is a secret: any value
// of a parameter whose name says so (secretQueryWords, secretQueryNames),
// and any other of eight bytes or more that looks like a token: letters
// and digits both, with no space, and no - or : such as a region
// (us-east-1) or a time (2024-01-01T00:00:00Z) has.
func secretQueryValue(key, value string) bool {
	if value == "" {
		return false
	}
	words := queryKeyWords(key)
	name := strings.Join(words, "")
	if plainQueryNames[name] {
		return false
	}
	if secretQueryNames[name] || slices.ContainsFunc(words, func(w string) bool { return secretQueryWords[w] }) {
		return true
	}
	if len(value) < 8 || strings.ContainsAny(value, " \t\r\n-:") {
		return false
	}
	letter := strings.ContainsFunc(value, func(r rune) bool { return ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') })
	digit := strings.ContainsFunc(value, func(r rune) bool { return '0' <= r && r <= '9' })
	return letter && digit
}

// queryKeyWords splits a parameter's name into lower case words at
// anything but a letter or digit, and where camelCase turns upper:
// "X-Amz-Signature" is x amz signature, "apiKey" is api key.
func queryKeyWords(key string) []string {
	var words []string
	var word []rune
	flush := func() {
		if len(word) > 0 {
			words = append(words, strings.ToLower(string(word)))
			word = word[:0]
		}
	}
	prevLower := false
	for _, r := range key {
		switch {
		case 'A' <= r && r <= 'Z':
			if prevLower {
				flush()
			}
			word = append(word, r)
			prevLower = false
		case ('a' <= r && r <= 'z') || ('0' <= r && r <= '9'):
			word = append(word, r)
			prevLower = true
		default:
			flush()
			prevLower = false
		}
	}
	flush()
	return words
}

// redactPieces replaces each run of text covered by pieces with
// "[redacted]", widened to whole characters.
func redactPieces(text string, pieces map[string]bool) string {
	if len(pieces) == 0 {
		return text
	}
	hidden := make([]bool, len(text))
	for i := 0; i+jobRedactRun <= len(text); i++ {
		if pieces[text[i:i+jobRedactRun]] {
			for k := i; k < i+jobRedactRun; k++ {
				hidden[k] = true
			}
		}
	}
	// A run can start or end inside a character: hide all of it.
	for i := 0; i < len(text); i++ {
		if !hidden[i] {
			continue
		}
		for k := i; k > 0 && !utf8.RuneStart(text[k]); k-- {
			hidden[k-1] = true
		}
		for i < len(text) && hidden[i] {
			i++
		}
		for ; i < len(text) && !utf8.RuneStart(text[i]); i++ {
			hidden[i] = true
		}
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		j := i
		for j < len(text) && hidden[j] == hidden[i] {
			j++
		}
		if hidden[i] {
			b.WriteString("[redacted]")
		} else {
			b.WriteString(text[i:j])
		}
		i = j
	}
	return b.String()
}

// JobTail keeps the end of a job's log as it's read from a target:
// JobOutputLimit bytes and the longest secret's length besides, so a
// secret the limit cuts through is still found whole. Where it cuts
// doesn't matter otherwise: JobOutput hides what's left of a cut secret.
type JobTail struct {
	keep int
	data []byte
}

func NewJobTail(secrets map[string]string) *JobTail {
	keep := JobOutputLimit
	for _, value := range secrets {
		keep = max(keep, JobOutputLimit+len(value))
	}
	return &JobTail{keep: keep}
}

func (t *JobTail) Write(p []byte) (int, error) {
	t.data = append(t.data, p...)
	if len(t.data) > 2*t.keep {
		t.data = append([]byte(nil), t.data[len(t.data)-t.keep:]...)
	}
	return len(p), nil
}

// Output is what's kept, as JobOutput gives it.
func (t *JobTail) Output(secrets map[string]string) string {
	data := t.data
	if len(data) > t.keep {
		data = data[len(data)-t.keep:]
	}
	return JobOutput(data, secrets)
}
