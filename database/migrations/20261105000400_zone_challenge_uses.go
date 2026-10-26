package migrations

func init() {
	App.MustRegister(statements("20261105000400", "zone_challenge_uses", zoneChallengeUsesUp, nil))
}
