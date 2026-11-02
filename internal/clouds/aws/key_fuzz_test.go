package aws

import "testing"

// FuzzParseKey checks a pasted access key or a keyless handout, whatever
// it is, is refused or read, never a crash.
func FuzzParseKey(f *testing.F) {
	f.Add([]byte(`{"access_key_id":"AKIAABCDEFGHIJKLMNOP","secret_access_key":"0123456789abcdefghijklmnopqrstuvwxyz0123"}`))
	f.Add([]byte(`{"access_key_id":""}`))
	f.Add([]byte("AKIA"))
	f.Add([]byte(`{"type":"loomplane_keyless","subject":"org:o:connection:c","audience":"sts.amazonaws.com","ticket":"t","settings":{"role_arn":"arn:aws:iam::111111111111:role/loomplane","region":"us-east-1"}}`))
	f.Add([]byte(`{"type":"loomplane_keyless","subject":"s","audience":"a","ticket":"t","settings":{"role_arn":"arn:aws:iam::1:role/"}}`))
	f.Fuzz(func(_ *testing.T, data []byte) { _, _ = ParseKey(data) })
}
