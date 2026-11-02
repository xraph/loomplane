package notifications

import "testing"

func TestKeylessNoticeNamesAWS(t *testing.T) {
	_, title, body, subject, _ := connectionCopy(ConnectionPayload{Name: "Prod AWS", Cloud: "aws", Status: noticeFailed}, "https://loomplane.example.com/clouds/accounts/conn-a")
	if title != "Prod AWS lost its keyless access to AWS" || subject != "Loomplane: Prod AWS can't reach AWS" ||
		body != "AWS no longer trusts Loomplane for this connection. Check the trust you set up there, then check the connection again." {
		t.Fatalf("title %q, subject %q, body %q", title, subject, body)
	}
}
