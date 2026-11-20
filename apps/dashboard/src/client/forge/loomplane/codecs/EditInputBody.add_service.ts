import type { Codec } from '../codec-runtime';
import { codec_EditInputBody_add_service_needed_by } from './EditInputBody.add_service.needed_by';
import { codec_EditInputBody_add_service_needs } from './EditInputBody.add_service.needs';

export const codec_EditInputBody_add_service: Codec = {"kind": "object", "fields": {"enabled_in": {"ts": "enabledIn"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "needed_by": {"ts": "neededBy", "codec": () => codec_EditInputBody_add_service_needed_by}, "needs": {"ts": "needs", "codec": () => codec_EditInputBody_add_service_needs}, "only": {"ts": "only"}, "port": {"ts": "port"}, "provider": {"ts": "provider"}, "public": {"ts": "public"}, "region": {"ts": "region"}, "repo": {"ts": "repo"}, "source": {"ts": "source"}, "target": {"ts": "target"}}, "required": ["name", "source"]};
