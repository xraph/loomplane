package cloudprofiles

// CompareCompensationSQL compares serving rows to the original operation ownership.
// Call it again inside acceptance after locking the original sync and environment.
const CompareCompensationSQL = `SELECT NOT EXISTS(
 SELECT 1 FROM jsonb_array_elements($3::jsonb) old
 LEFT JOIN loomplane_cloud_sync_bindings c ON c.sync_id=$2 AND c.binding_id=old->>'id' AND c.org_id=$1
 LEFT JOIN loomplane_bindings b ON b.org_id=$1 AND b.id=old->>'id'
 WHERE b.id IS NULL OR (to_jsonb(b)-'observed_at'-'drift_message'-'replaced') IS DISTINCT FROM (coalesce(c.after_value,old)-'replaced')
 ) AND NOT EXISTS(
 SELECT 1 FROM loomplane_bindings b WHERE b.org_id=$1 AND b.environment_id=$4
 AND NOT EXISTS(SELECT 1 FROM jsonb_array_elements($3::jsonb) old WHERE old->>'id'=b.id)
 )`
