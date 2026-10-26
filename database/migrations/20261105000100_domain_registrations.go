package migrations

func init() {
	App.MustRegister(statements("20261105000100", "domain_registrations", domainRegistrationsUp, domainRegistrationsDown))
}
