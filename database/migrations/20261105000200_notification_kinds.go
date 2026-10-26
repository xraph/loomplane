package migrations

func init() {
	App.MustRegister(statements("20261105000200", "notification_kinds", notificationKindsUp, nil))
}
