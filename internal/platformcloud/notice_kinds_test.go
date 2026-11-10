package platformcloud

import (
	"slices"
	"testing"

	"github.com/xraph/loomplane/internal/notifications"
)

// Webhook subscribers can only opt in to events the notifications catalog
// lists, and a notice goes out as its kind plus ".created".
func TestNoticeKindsAreInTheWebhookCatalog(t *testing.T) {
	names := notifications.WebhookEventNames()
	for _, kind := range []string{NoticeSuspended, NoticeRestored, NoticeWaitingCapacity} {
		if !slices.Contains(names, kind+".created") {
			t.Errorf("%s.created is not in the webhook catalog", kind)
		}
	}
}
