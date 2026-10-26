package migrations

// Outbox rows were all deployment notices until domain notices joined them.
var notificationKindsUp = []string{
	`ALTER TABLE loomplane_notification_outbox ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'deployment'`,
}

func init() {
	App.MustRegister(statements("20261105000200", "notification_kinds", notificationKindsUp, nil))
}
