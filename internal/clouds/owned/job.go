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
		for _, part := range sensitiveParts(value) {
			for i := 0; i+jobRedactRun <= len(part); i++ {
				pieces[part[i:i+jobRedactRun]] = true
			}
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
// (secretQueryValue), each as decoded and as the address spells it. Its
// host, path and an ordinary user, such as postgres, are left, so output
// naming them stays readable. An address without a password is secret
// whole, since its path can hold the secret (a bot's token, a webhook's
// key), and its token user and secret query values count on their own
// too. Anything else is secret whole. A value of several lines is covered
// line by line too, since its pieces include each line's.
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

// urlTokenUser is the shortest user of an address that is a secret
// itself, such as a token a Git host takes as the user. Shorter ones are
// names, such as postgres.
const urlTokenUser = 16

// secretQueryKeys are words in a query parameter's name that make its
// value a secret.
var secretQueryKeys = []string{"token", "key", "secret", "password", "passwd", "pwd", "auth", "signature", "sig", "credential"}

// secretQueryValue reports whether a query value is a secret: any value
// of a parameter whose name says so, such as api_key or access_token, and
// any other of eight bytes or more that looks like a token rather than a
// word. A word, such as verify-full or a service's name, is only lower
// case letters and - _ . and a value with a space in it isn't a token.
func secretQueryValue(key, value string) bool {
	if value == "" {
		return false
	}
	key = strings.ToLower(key)
	for _, word := range secretQueryKeys {
		if strings.Contains(key, word) {
			return true
		}
	}
	if len(value) < 8 || strings.ContainsAny(value, " \t\r\n") {
		return false
	}
	return strings.ContainsFunc(value, func(r rune) bool {
		return ('0' <= r && r <= '9') || ('A' <= r && r <= 'Z')
	})
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
