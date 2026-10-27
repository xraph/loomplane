package gcp

import "testing"

// FuzzParseKey checks a pasted credential, whatever it is, is refused or
// read, never a crash.
func FuzzParseKey(f *testing.F) {
	f.Add([]byte(`{"type":"service_account","project_id":"p","client_email":"a@p.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\n-----END PRIVATE KEY-----\n"}`))
	f.Add([]byte(`{"type":"authorized_user"}`))
	f.Add([]byte(`{"type":"authorized_user","client_id":"c.apps.googleusercontent.com","client_secret":"s","refresh_token":"1//r","project_id":"shop-prod","client_email":"ada@example.com","token_uri":"https://oauth2.googleapis.com/token"}`))
	f.Add([]byte("not json"))
	f.Fuzz(func(_ *testing.T, data []byte) { _, _ = ParseKey(data) })
}
