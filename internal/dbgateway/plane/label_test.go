package plane

import (
	"errors"
	"strings"
	"testing"
)

func TestParseLabel(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    Label
		wantErr bool
	}{
		{name: "db only", in: "orders", want: Label{DB: "orders"}},
		{name: "db and branch", in: "orders--feat-x", want: Label{DB: "orders", Branch: "feat-x"}},
		{name: "single hyphens in db", in: "my-orders", want: Label{DB: "my-orders"}},
		{name: "digit-led branch", in: "orders--2024", want: Label{DB: "orders", Branch: "2024"}},
		{name: "longest label", in: "a" + strings.Repeat("b", 39) + "--" + strings.Repeat("c", 21), want: Label{DB: "a" + strings.Repeat("b", 39), Branch: strings.Repeat("c", 21)}},
		{name: "uppercase", in: "Orders", wantErr: true},
		{name: "uppercase branch", in: "orders--Feat", wantErr: true},
		{name: "second separator", in: "or--d--x", wantErr: true},
		{name: "empty", in: "", wantErr: true},
		{name: "64 characters", in: "a" + strings.Repeat("b", 39) + "--" + strings.Repeat("c", 22), wantErr: true},
		{name: "db over 40 characters", in: "a" + strings.Repeat("b", 40), wantErr: true},
		{name: "leading digit", in: "1orders", wantErr: true},
		{name: "empty branch", in: "orders--", wantErr: true},
		{name: "empty db", in: "--feat", wantErr: true},
		{name: "branch led by hyphen", in: "orders---feat", wantErr: true},
		{name: "branch ending in hyphen", in: "orders--feat-", wantErr: true},
		{name: "underscore", in: "my_orders", wantErr: true},
		{name: "dot", in: "orders.x", wantErr: true},
		{name: "at sign", in: "app@orders", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseLabel(tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParseLabel(%q) = %+v, want an error", tt.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseLabel(%q) error: %v", tt.in, err)
			}
			if got != tt.want {
				t.Fatalf("ParseLabel(%q) = %+v, want %+v", tt.in, got, tt.want)
			}
			if s := got.String(); s != tt.in {
				t.Fatalf("String() = %q, want %q", s, tt.in)
			}
		})
	}
}

func TestLabelFromHost(t *testing.T) {
	const suffix Suffix = "us-east-1.db.example.com"
	tests := []struct {
		name    string
		host    string
		want    Label
		wantErr error // matched with errors.Is when set
		anyErr  bool  // any error, not necessarily ErrNoRoute
	}{
		{name: "plain", host: "orders.us-east-1.db.example.com", want: Label{DB: "orders"}},
		{name: "port", host: "orders.us-east-1.db.example.com:5432", want: Label{DB: "orders"}},
		{name: "branch", host: "orders--feat-x.us-east-1.db.example.com", want: Label{DB: "orders", Branch: "feat-x"}},
		{name: "case-insensitive host", host: "Orders.US-East-1.DB.Example.com", want: Label{DB: "orders"}},
		{name: "trailing dot", host: "orders.us-east-1.db.example.com.", want: Label{DB: "orders"}},
		{name: "wrong suffix", host: "orders.eu-west-1.db.example.com", wantErr: ErrNoRoute},
		{name: "bare suffix", host: "us-east-1.db.example.com", wantErr: ErrNoRoute},
		{name: "no dot before suffix", host: "xus-east-1.db.example.com", wantErr: ErrNoRoute},
		{name: "two labels before suffix", host: "a.b.us-east-1.db.example.com", wantErr: ErrNoRoute},
		{name: "empty host", host: "", wantErr: ErrNoRoute},
		{name: "bad label under right suffix", host: "1orders.us-east-1.db.example.com", anyErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := suffix.LabelFromHost(tt.host)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("LabelFromHost(%q) error = %v, want %v", tt.host, err, tt.wantErr)
				}
			case tt.anyErr:
				if err == nil {
					t.Fatalf("LabelFromHost(%q) = %+v, want an error", tt.host, got)
				}
			default:
				if err != nil {
					t.Fatalf("LabelFromHost(%q) error: %v", tt.host, err)
				}
				if got != tt.want {
					t.Fatalf("LabelFromHost(%q) = %+v, want %+v", tt.host, got, tt.want)
				}
			}
		})
	}

	if _, err := Suffix("").LabelFromHost("orders."); !errors.Is(err, ErrNoRoute) {
		t.Fatalf("empty suffix: error = %v, want ErrNoRoute", err)
	}
}

func TestRouteKey(t *testing.T) {
	const suffix Suffix = "us-east-1.db.example.com"
	tests := []struct {
		name     string
		host     string
		startup  map[string]string
		want     Label
		wantRole string
		wantErr  error
	}{
		{
			name:     "logical host",
			host:     "orders.us-east-1.db.example.com:5432",
			startup:  map[string]string{"user": "app"},
			want:     Label{DB: "orders"},
			wantRole: "app",
		},
		{
			name:     "logical host wins over options and user",
			host:     "orders.us-east-1.db.example.com",
			startup:  map[string]string{"user": "app@other", "options": "endpoint=another"},
			want:     Label{DB: "orders"},
			wantRole: "app@other",
		},
		{
			name:     "user repeats the host label",
			host:     "orders.us-east-1.db.example.com",
			startup:  map[string]string{"user": "app@orders"},
			want:     Label{DB: "orders"},
			wantRole: "app",
		},
		{
			name:     "endpoint in options with other settings",
			host:     "gw.run.app",
			startup:  map[string]string{"user": "app", "options": "-c foo=bar endpoint=orders--feat-x"},
			want:     Label{DB: "orders", Branch: "feat-x"},
			wantRole: "app",
		},
		{
			name:     "endpoint after -c",
			host:     "gw.run.app",
			startup:  map[string]string{"user": "app", "options": "-c endpoint=orders"},
			want:     Label{DB: "orders"},
			wantRole: "app",
		},
		{
			name:     "endpoint attached to -c",
			host:     "gw.run.app",
			startup:  map[string]string{"user": "app", "options": "-cendpoint=orders"},
			want:     Label{DB: "orders"},
			wantRole: "app",
		},
		{
			name:     "extra whitespace in options",
			host:     "gw.run.app",
			startup:  map[string]string{"user": "app", "options": "  -c   foo=bar \t endpoint=orders  "},
			want:     Label{DB: "orders"},
			wantRole: "app",
		},
		{
			name:     "options beat user label",
			host:     "gw.run.app",
			startup:  map[string]string{"user": "app@other", "options": "endpoint=orders"},
			want:     Label{DB: "orders"},
			wantRole: "app@other",
		},
		{
			name:     "options without endpoint fall through to user",
			host:     "gw.run.app",
			startup:  map[string]string{"user": "app@orders", "options": "-c foo=bar"},
			want:     Label{DB: "orders"},
			wantRole: "app",
		},
		{
			name:     "user label",
			host:     "gw.run.app",
			startup:  map[string]string{"user": "app@orders"},
			want:     Label{DB: "orders"},
			wantRole: "app",
		},
		{
			name:     "user label with branch",
			host:     "gw.run.app:5432",
			startup:  map[string]string{"user": "app@orders--feat-x"},
			want:     Label{DB: "orders", Branch: "feat-x"},
			wantRole: "app",
		},
		{
			name:     "role containing an at sign",
			host:     "gw.run.app",
			startup:  map[string]string{"user": "me@corp.example@orders"},
			want:     Label{DB: "orders"},
			wantRole: "me@corp.example",
		},
		{
			name:    "wrong suffix and no hint",
			host:    "orders.eu-west-1.db.example.com",
			startup: map[string]string{"user": "app"},
			wantErr: ErrNoRoute,
		},
		{
			name:    "nil startup",
			host:    "gw.run.app",
			wantErr: ErrNoRoute,
		},
		{
			name:    "user with at sign but no role",
			host:    "gw.run.app",
			startup: map[string]string{"user": "@orders"},
			wantErr: ErrNoRoute,
		},
		{
			name:    "user with trailing at sign",
			host:    "gw.run.app",
			startup: map[string]string{"user": "app@"},
			wantErr: ErrNoRoute,
		},
		{
			name:    "invalid label under the right suffix",
			host:    "Bad_1.us-east-1.db.example.com",
			startup: map[string]string{"user": "app@orders"},
			wantErr: ErrNoRoute,
		},
		{
			name:    "invalid endpoint in options",
			host:    "gw.run.app",
			startup: map[string]string{"user": "app@orders", "options": "endpoint=Or--d--x"},
			wantErr: ErrNoRoute,
		},
		{
			name:    "empty endpoint in options",
			host:    "gw.run.app",
			startup: map[string]string{"user": "app", "options": "endpoint="},
			wantErr: ErrNoRoute,
		},
		{
			name:    "invalid label in user",
			host:    "gw.run.app",
			startup: map[string]string{"user": "app@1orders"},
			wantErr: ErrNoRoute,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, role, err := RouteKey(suffix, tt.host, tt.startup)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("RouteKey error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("RouteKey error: %v", err)
			}
			if got != tt.want || role != tt.wantRole {
				t.Fatalf("RouteKey = (%+v, %q), want (%+v, %q)", got, role, tt.want, tt.wantRole)
			}
		})
	}
}
