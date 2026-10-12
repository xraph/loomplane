package deployments

import "testing"

func TestRDSRevision(t *testing.T) {
	for _, c := range []struct{ engine, version, want string }{
		{"postgres", "17.2", "postgres/17"},
		{"postgres", "17", "postgres/17"},
		{"mysql", "8.4.3", "mysql/8.4"},
		{"mysql", "8.0", "mysql/8.0"},
	} {
		if got := rdsRevision(c.engine, c.version); got != c.want {
			t.Errorf("rdsRevision(%q, %q) = %q, want %q", c.engine, c.version, got, c.want)
		}
	}
}

func TestRDSURL(t *testing.T) {
	const host = "lp-db.abc.us-east-1.rds.amazonaws.com"
	if got := rdsURL("postgres", "p/w", host, 5432, "db"); got != "postgresql://app:p%2Fw@"+host+":5432/db?sslmode=require" {
		t.Errorf("postgres %q", got)
	}
	if got := rdsURL("mysql", "pw", host, 3306, "db"); got != "mysql://app:pw@"+host+":3306/db?tls=true" {
		t.Errorf("mysql %q", got)
	}
}
