import type { Codec } from '../codec-runtime';
import { codec_LocationVerificationInputBody_failure_domains_items_dependency_ids } from './LocationVerificationInputBody.failure_domains.items.dependency_ids';

export const codec_LocationVerificationInputBody_failure_domains_items: Codec = {"kind": "object", "fields": {"dependency_ids": {"ts": "dependencyIds", "codec": () => codec_LocationVerificationInputBody_failure_domains_items_dependency_ids}, "id": {"ts": "id"}, "kind": {"ts": "kind"}}, "required": ["id", "kind"]};
