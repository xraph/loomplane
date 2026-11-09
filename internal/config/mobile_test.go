package config

import (
	"slices"
	"strings"
	"testing"
)

func TestMobileDefaultsAreTheStoreApp(t *testing.T) {
	c, err := FromSource(mapGetter{"database.url": testDSN})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mobile.AndroidPackage != "com.loomplane.app" || !slices.Equal(c.Mobile.AndroidCertFingerprints, DefaultAndroidCertFingerprints) {
		t.Fatalf("Mobile = %+v", c.Mobile)
	}
}

func TestMobileFingerprintsReplaceTheDefaults(t *testing.T) {
	lower := strings.TrimSuffix(strings.Repeat("ab:", 32), ":")
	c, err := FromSource(mapGetter{
		"database.url": testDSN, "mobile.android_package": "com.example.app",
		"mobile.android_cert_fingerprints": " " + lower + " ,",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Mobile.AndroidPackage != "com.example.app" || !slices.Equal(c.Mobile.AndroidCertFingerprints, []string{strings.ToUpper(lower)}) {
		t.Fatalf("Mobile = %+v", c.Mobile)
	}
	// Validating upper-cases in place; the defaults must stay untouched.
	if DefaultAndroidCertFingerprints[0][0] != 'C' {
		t.Fatal("the defaults changed")
	}
}

func TestMobileRejectsWhatAndroidWouldNot(t *testing.T) {
	good := strings.TrimSuffix(strings.Repeat("AB:", 32), ":")
	tests := []struct {
		name, pkg, fingerprint, want string
	}{
		{"one-segment package", "loomplane", good, "android_package"},
		{"package with a dash", "com.loom-plane.app", good, "android_package"},
		{"SHA-1 fingerprint", "com.example.app", strings.TrimSuffix(strings.Repeat("AB:", 20), ":"), "SHA-256"},
		{"no colons", "com.example.app", strings.Repeat("AB", 32), "SHA-256"},
		{"not hex", "com.example.app", "ZZ" + good[2:], "SHA-256"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := MobileConfig{AndroidPackage: tt.pkg, AndroidCertFingerprints: []string{tt.fingerprint}}
			if err := m.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate = %v, want an error containing %q", err, tt.want)
			}
		})
	}
}
