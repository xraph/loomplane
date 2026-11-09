import type { Codec } from '../codec-runtime';
import { codec_FailureDomain_dependency_ids } from './FailureDomain.dependency_ids';

export const codec_FailureDomain: Codec = {"kind": "object", "fields": {"dependency_ids": {"ts": "dependencyIds", "codec": () => codec_FailureDomain_dependency_ids}, "id": {"ts": "id"}, "kind": {"ts": "kind"}, "source": {"ts": "source"}, "verified": {"ts": "verified"}}, "required": ["id", "kind", "source", "verified"]};
