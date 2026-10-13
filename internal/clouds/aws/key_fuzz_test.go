package aws

import "testing"

// FuzzParseKey checks a pasted access key, whatever it is, is refused or
// read, never a crash.
func FuzzParseKey(f *testing.F) {
	f.Add([]byte(`{"access_key_id":"AKIAABCDEFGHIJKLMNOP","secret_access_key":"0123456789abcdefghijklmnopqrstuvwxyz0123"}`))
	f.Add([]byte(`{"access_key_id":""}`))
	f.Add([]byte("AKIA"))
	f.Fuzz(func(_ *testing.T, data []byte) { _, _ = ParseKey(data) })
}
