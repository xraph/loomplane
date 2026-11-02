package recovery

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

func nativeRetentionPolicyRequestKey(p *NativeRetentionPolicy) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("native-retention-v1\x00%s\x00%s\x00%s\x00%d\x00%d", p.OrgID, p.SourceID, p.ID, p.Revision, p.NextAt.UnixMicro())))
	return "native_retention_" + hex.EncodeToString(sum[:])
}
