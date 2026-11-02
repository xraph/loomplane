package nodeproto

import (
	"bytes"
	"testing"
)

func FuzzReadHeader(f *testing.F) {
	var buf bytes.Buffer
	_ = WriteHeader(&buf, StreamHeader{Endpoint: EndpointProbe, OrgID: "o"})
	f.Add(buf.Bytes())
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Add([]byte{0, 0, 0, 2, '{', '}'})
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ReadHeader(bytes.NewReader(data))
	})
}

func FuzzDecodeControl(f *testing.F) {
	f.Add([]byte(`{"type":"hello","version":1}`))
	f.Add([]byte(`{"type":"future"}`))
	f.Add([]byte(`[]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = DecodeControl(data)
	})
}
