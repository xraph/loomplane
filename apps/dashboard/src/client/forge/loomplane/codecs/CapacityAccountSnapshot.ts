import type { Codec } from '../codec-runtime';
import { codec_CapacityQuota } from './CapacityQuota';
import { codec_CapacityResources } from './CapacityResources';

export const codec_CapacityAccountSnapshot: Codec = {"kind": "object", "fields": {"account_id": {"ts": "accountId"}, "checked_at": {"ts": "checkedAt"}, "provider": {"ts": "provider"}, "provisioned": {"ts": "provisioned", "codec": () => codec_CapacityResources}, "quota": {"ts": "quota", "codec": () => codec_CapacityQuota}}, "required": ["account_id", "checked_at", "provider", "provisioned", "quota"]};
