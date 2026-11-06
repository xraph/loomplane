package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DefaultAndroidPackage is the store app's Android package name.
const DefaultAndroidPackage = "com.loomplane.app"

// DefaultAndroidCertFingerprints are the SHA-256 fingerprints of the
// certificates the store app is signed with.
var DefaultAndroidCertFingerprints = []string{
	"C1:DD:36:A1:3B:CC:E7:04:75:52:60:03:2A:FF:72:94:63:44:B5:A4:21:7A:02:D4:B8:2B:32:AC:96:C2:7C:3B",
	"92:5E:CF:EC:F2:4A:DD:4A:5D:EB:E4:2E:80:9F:87:4D:6B:FA:F3:D7:28:59:CC:22:4B:7C:66:FC:3B:7A:85:37",
	"7A:87:08:EF:4A:B5:E7:77:F5:93:D0:60:F3:D2:05:3E:BE:83:A9:F9:28:2E:47:D2:A3:14:21:3C:E8:CF:7C:69",
}

// MobileConfig names the Android app this server vouches for in
// /.well-known/assetlinks.json, so links to it open in the app and the app
// can use passwords saved for it. The defaults are the store app. Set both
// when you ship your own build.
type MobileConfig struct {
	// AndroidPackage is the app's package name.
	AndroidPackage string
	// AndroidCertFingerprints are the SHA-256 fingerprints of the
	// certificates that sign the app, as colon-separated hex pairs.
	AndroidCertFingerprints []string
}

// certFingerprint is a SHA-256 digest as keytool prints it.
var certFingerprint = regexp.MustCompile(`^[0-9A-F]{2}(:[0-9A-F]{2}){31}$`)

// androidPackage is a package name as Android allows it.
var androidPackage = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*(\.[A-Za-z][A-Za-z0-9_]*)+$`)

// Validate upper-cases the fingerprints, which Android compares as written,
// and refuses a package name or fingerprint that isn't one.
func (m *MobileConfig) Validate() error {
	if !androidPackage.MatchString(m.AndroidPackage) {
		return fmt.Errorf("mobile.android_package must be an Android package name such as com.example.app, got %q", m.AndroidPackage)
	}
	if len(m.AndroidCertFingerprints) == 0 {
		return errors.New("mobile.android_cert_fingerprints needs at least one fingerprint")
	}
	for i, f := range m.AndroidCertFingerprints {
		f = strings.ToUpper(f)
		if !certFingerprint.MatchString(f) {
			return fmt.Errorf("mobile.android_cert_fingerprints must be SHA-256 fingerprints (32 hex pairs joined by colons), got %q", m.AndroidCertFingerprints[i])
		}
		m.AndroidCertFingerprints[i] = f
	}
	return nil
}
