import type { Codec } from '../codec-runtime';
import { codec_LocationInputBody_failure_domains_items_dependency_ids } from './LocationInputBody.failure_domains.items.dependency_ids';

export const codec_LocationInputBody_failure_domains_items: Codec = {"kind": "object", "fields": {"dependency_ids": {"ts": "dependencyIds", "codec": () => codec_LocationInputBody_failure_domains_items_dependency_ids}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "source": {"ts": "source"}, "verified": {"ts": "verified"}}, "required": ["id", "kind", "source", "verified"]};
