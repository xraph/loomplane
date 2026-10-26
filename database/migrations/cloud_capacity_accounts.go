package migrations

var cloudCapacityAccountsUp = []string{
	`CREATE TABLE IF NOT EXISTS loomplane_cloud_capacity_accounts (
 org_id TEXT NOT NULL, provider TEXT NOT NULL, account_id TEXT NOT NULL,
 servers BIGINT CHECK(servers>=0), cpu_millis BIGINT CHECK(cpu_millis>=0),
 memory_bytes BIGINT CHECK(memory_bytes>=0), storage_bytes BIGINT CHECK(storage_bytes>=0),
 quota_servers BIGINT CHECK(quota_servers>=0), quota_cpu_millis BIGINT CHECK(quota_cpu_millis>=0),
 quota_memory_bytes BIGINT CHECK(quota_memory_bytes>=0), quota_storage_bytes BIGINT CHECK(quota_storage_bytes>=0),
 checked_at TIMESTAMPTZ NOT NULL, body JSONB NOT NULL,
 PRIMARY KEY(org_id,provider,account_id))`,
}
var cloudCapacityAccountsDown = []string{`DROP TABLE IF EXISTS loomplane_cloud_capacity_accounts`}
