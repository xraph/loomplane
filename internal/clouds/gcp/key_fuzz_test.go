package gcp

import "testing"

// FuzzParseKey checks a pasted credential or a keyless handout, whatever
// it is, is refused or read, never a crash.
func FuzzParseKey(f *testing.F) {
	f.Add([]byte(`{"type":"service_account","project_id":"p","client_email":"a@p.iam.gserviceaccount.com","private_key":"-----BEGIN PRIVATE KEY-----\n-----END PRIVATE KEY-----\n"}`))
	f.Add([]byte(`{"type":"authorized_user"}`))
	f.Add([]byte(`{"type":"authorized_user","client_id":"c.apps.googleusercontent.com","client_secret":"s","refresh_token":"1//r","project_id":"shop-prod","client_email":"ada@example.com","token_uri":"https://oauth2.googleapis.com/token"}`))
	f.Add([]byte("not json"))
	f.Add([]byte(`{"type":"loomplane_keyless","subject":"org:o:connection:c","audience":"//iam.googleapis.com/projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/loomplane","ticket":"t","settings":{"project_id":"shop-prod","workload_provider":"projects/123456789012/locations/global/workloadIdentityPools/loomplane/providers/loomplane","service_account":"loomplane@shop-prod.iam.gserviceaccount.com"}}`))
	f.Add([]byte(`{"type":"loomplane_keyless","subject":"s","audience":"a","ticket":"t","settings":{"project_id":"shop-prod","workload_provider":"projects/1/locations/global/workloadIdentityPools/x/providers/y"}}`))
	f.Fuzz(func(_ *testing.T, data []byte) { _, _ = ParseKey(data) })
}
