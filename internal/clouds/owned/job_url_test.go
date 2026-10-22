package owned

import (
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
)

const (
	// A token used as an address's user, with the placeholder password
	// some Git hosts take beside it.
	userToken = "tok-Ab12Cd34Ef56Gh78Ij90Kl12Mn34"
	gitRemote = "https://" + userToken + ":x-oauth-basic@git.example.com/acme/app.git"
	// A token as the whole of an address's user.
	registryToken = "Qr7sT2uV9wX4yZ1aB6cD"
	registry      = "https://" + registryToken + "@registry.example.com/v2/"
	// A secret in a query string, beside values that aren't.
	queryKey = "Zx9Yw8Vu7Ts6Rq5Po4Nm"
	webhook  = "https://hooks.example.com/notify?channel=ops&api_key=" + queryKey + "&mode=verify-full&sig=a1B2&next=Lm3kQ9zR"
	// A token in an address's path, beside a query value that looks
	// secret, and a token user beside a secret path.
	pathToken  = "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsaw"
	botAPI     = "https://api.chat.example/bot123456:" + pathToken + "/sendMessage?chat_id=12345678&next=Lm3kQ9zR"
	secretPath = "sEcr3tPathToken99"
	userRepo   = "https://" + registryToken + "@git.example.com/acme/" + secretPath + "/app.git"
)

func TestSensitivePartsOfAnAddress(t *testing.T) {
	for value, want := range map[string][]string{
		// A password, and a user that looks like a token.
		gitRemote: {"x-oauth-basic", userToken},
		// Without a password the whole address stays secret, since its
		// path can be one too, and a token user counts on its own.
		registry: {registry, registryToken},
		// Query values: one under a secret's name at any length, others
		// of eight or more that look like a token. Not a plain word.
		webhook: {webhook, queryKey, "a1B2", "Lm3kQ9zR"},
		botAPI:  {botAPI, "12345678", "Lm3kQ9zR"},
		// An escaped value counts as it's spelled too.
		"https://hooks.example.com/x?token=Ab%2BCd%2FEf12": {"https://hooks.example.com/x?token=Ab%2BCd%2FEf12", "Ab+Cd/Ef12", "Ab%2BCd%2FEf12"},
		// A short user is an ordinary word, beside a password.
		dsn: {"pg-Secret-Value-17x"},
		// An address with nothing secret in it is all secret, as before.
		"postgres://postgres@db:5432/app": {"postgres://postgres@db:5432/app"},
		"not a url":                       {"not a url"},
	} {
		got := sensitiveParts(value)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			t.Fatalf("%s: got %q, want %q", value, got, want)
		}
	}
}

// shows reports four bytes of part that output shows outside its
// redactions.
func shows(output, part string) string {
	for _, text := range strings.Split(output, "[redacted]") {
		for start := 0; start+4 <= len(part); start++ {
			if piece := part[start : start+4]; strings.Contains(text, piece) {
				return piece
			}
		}
	}
	return ""
}

// TestJobOutputNeverShowsFourBytesOfAURLsToken writes addresses with a
// token as their user or in their query, cut at random points between
// other output, and checks no four bytes of a token are shown.
func TestJobOutputNeverShowsFourBytesOfAURLsToken(t *testing.T) {
	random := rand.New(rand.NewPCG(3, 4))
	secrets := map[string]string{"GIT_REMOTE": gitRemote, "REGISTRY": registry, "WEBHOOK": webhook, "BOT_API": botAPI, "USER_REPO": userRepo}
	values := []string{gitRemote, registry, webhook, botAPI, userRepo}
	tokens := []string{userToken, registryToken, queryKey, "Lm3kQ9zR", pathToken, secretPath}
	noise := []string{"warn: slow\n", "ok\n", "\n", "connecting to postgres://db:5432\n", "pushing to git.example.com/acme/app.git\n"}
	for round := range 300 {
		var writes []string
		for range 2 + random.IntN(12) {
			value := values[random.IntN(len(values))]
			start := random.IntN(len(value))
			end := start + 1 + random.IntN(len(value)-start)
			piece := value[start:end]
			for piece != "" {
				n := 1 + random.IntN(len(piece))
				writes = append(writes, piece[:n], noise[random.IntN(len(noise))])
				piece = piece[n:]
			}
		}
		output := tailed(secrets, writes...)
		for _, token := range tokens {
			if piece := shows(output, token); piece != "" {
				t.Fatalf("round %d: output shows %q of %s", round, piece, token)
			}
		}
	}
}

func TestJobOutputLeavesAnAddressesOrdinaryWords(t *testing.T) {
	// Addresses with a password: what isn't secret in them stays.
	secrets := map[string]string{"GIT_REMOTE": gitRemote, "DATABASE_URL": dsn}
	const ordinary = "connecting to postgres://db:5432\n" +
		"pushing to git.example.com/acme/app.git\n" +
		"lp-inst-db:5432/app__postgres sslmode=disable: connected successfully\n" +
		"user loomplane signed in\n"
	if output := tailed(secrets, ordinary); output != ordinary {
		t.Fatalf("output: %q", output)
	}
	// Split, a token as the user goes and the rest stays.
	at := strings.Index(gitRemote, "Ef56")
	output := tailed(secrets, "push "+gitRemote[:at], "warn: slow\n", gitRemote[at:]+"\n")
	if want := "push https://[redacted]warn: slow\n[redacted]:[redacted]@git.example.com/acme/app.git\n"; output != want {
		t.Fatalf("output: %q", output)
	}
}
