package gcp

import "testing"

// FuzzParseKey checks a pasted credential, whatever it is, is refused or
// read, never a crash.
func FuzzParseKey(f *testing.F) {
	f.Add([]byte(`{"type":"service_account","project_id":"p","client_email":"a@p.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\n-----END PRIVATE KEY-----\n"}`))
	f.Add([]byte(`{"type":"authorized_user"}`))
	f.Add([]byte("not json"))
	f.Fuzz(func(_ *testing.T, data []byte) { _, _ = ParseKey(data) })
}
