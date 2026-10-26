package keyless

import (
	"strings"
	"testing"
)

func TestSubjectFormat(t *testing.T) {
	if got := Subject("org_1", "conn_2"); got != "org:org_1:connection:conn_2" {
		t.Fatalf("Subject = %q, want org:org_1:connection:conn_2", got)
	}
}

// A service account key, or anything else that isn't a keyless handout,
// isn't one of ours, so the cloud client treats it the way it always has.
func TestParseIgnoresOtherJSON(t *testing.T) {
	for name, data := range map[string]string{
		"service account key": `{"type":"service_account","project_id":"p","private_key":"-----BEGIN PRIVATE KEY-----"}`,
		"no type":             `{"subject":"org:o:connection:c"}`,
		"a JSON string":       `"loomplane_keyless"`,
		"not JSON":            `AKIA1234:secret`,
		"empty":               ``,
	} {
		h, ok, err := Parse([]byte(data))
		if ok || err != nil {
			t.Errorf("%s: ok = %v, err = %v, want false and nil", name, ok, err)
		}
		if h.Subject != "" || h.Ticket != "" {
			t.Errorf("%s: handout = %+v, want empty", name, h)
		}
	}
}

func TestParseNeedsSubjectAudienceAndTicket(t *testing.T) {
	h, ok, err := Parse([]byte(`{"type":"loomplane_keyless","subject":"org:o:connection:c","audience":"sts.amazonaws.com","ticket":"secret-ticket","settings":{"role_arn":"arn"}}`))
	if !ok || err != nil {
		t.Fatalf("full handout: ok = %v, err = %v", ok, err)
	}
	if h.Type != Type || h.Subject != "org:o:connection:c" || h.Audience != "sts.amazonaws.com" || h.Ticket != "secret-ticket" || h.Settings["role_arn"] != "arn" {
		t.Fatalf("handout = %+v", h)
	}

	for name, data := range map[string]string{
		"no subject":  `{"type":"loomplane_keyless","audience":"sts.amazonaws.com","ticket":"secret-ticket"}`,
		"no audience": `{"type":"loomplane_keyless","subject":"org:o:connection:c","ticket":"secret-ticket"}`,
		"no ticket":   `{"type":"loomplane_keyless","subject":"org:o:connection:c","audience":"sts.amazonaws.com"}`,
		"bad field":   `{"type":"loomplane_keyless","subject":7,"audience":"a","ticket":"secret-ticket"}`,
	} {
		_, ok, err := Parse([]byte(data))
		if !ok || err == nil {
			t.Errorf("%s: ok = %v, err = %v, want true and an error", name, ok, err)
			continue
		}
		if strings.Contains(err.Error(), "secret-ticket") {
			t.Errorf("%s: the error repeats the ticket", name)
		}
	}
}
