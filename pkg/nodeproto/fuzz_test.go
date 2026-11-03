package nodeproto

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"testing"
	"time"
)

func FuzzReadHeader(f *testing.F) {
	var buf bytes.Buffer
	_ = WriteHeader(&buf, StreamHeader{Endpoint: EndpointProbe, OrgID: "o"})
	f.Add(buf.Bytes())
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = ReadHeader(bytes.NewReader(data))
	})
}

func FuzzDecodeControl(f *testing.F) {
	f.Add([]byte(`{"type":"hello","version":1}`))
	f.Add([]byte(`{"type":"future"}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		_, _ = DecodeControl(data)
	})
}

func FuzzVerify(f *testing.F) {
	pub, priv, _ := ed25519.GenerateKey(nil)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	good, _ := Sign(priv, Challenge{NodeID: "n", Audience: "a", Nonce: "x", IssuedAt: now})
	b, _ := json.Marshal(good)
	f.Add(b)
	f.Add([]byte(`{"challenge":{"issued_at":"9999-12-31T23:59:59-23:59"},"signature":"` + zeroSig + `"}`))
	f.Add([]byte(`{"challenge":{"issued_at":"0000-01-01T00:00:00+23:59"},"signature":"` + zeroSig + `"}`))
	f.Fuzz(func(_ *testing.T, data []byte) {
		var a Assertion
		if json.Unmarshal(data, &a) != nil {
			return
		}
		_ = Verify(pub, a, "a", now)
	})
}
