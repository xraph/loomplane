package security

var (
	// AIRead is held by viewers and up: gateway, settings (no secrets), usage, agents and conversations.
	AIRead = Permission{"ai", "read"}
	// AIEdit is held by developers and up: build agents, flows, skills, tools and knowledge bases, and write the gateway outside production.
	AIEdit = Permission{"ai", "edit"}
	// AIToolSecret sets secrets for the tools of agents the holder can edit.
	AIToolSecret = Permission{"ai", "tool_secret"}
	// AIPublishNonProduction publishes to non-production without approval.
	AIPublishNonProduction = Permission{"ai", "publish_nonproduction"}
	// AIManage is held by admins and up: workspace AI settings, provider keys, budgets and production gateway writes.
	AIManage = Permission{"ai", "manage"}
)
