import type { Codec } from '../codec-runtime';
import { codec_NewServiceInput_needed_by } from './NewServiceInput.needed_by';
import { codec_NewServiceInput_needs } from './NewServiceInput.needs';

export const codec_NewServiceInput: Codec = {"kind": "object", "fields": {"cloud": {"ts": "cloud"}, "cloud_target": {"ts": "cloudTarget"}, "enabled_in": {"ts": "enabledIn"}, "kind": {"ts": "kind"}, "name": {"ts": "name"}, "needed_by": {"ts": "neededBy", "codec": () => codec_NewServiceInput_needed_by}, "needs": {"ts": "needs", "codec": () => codec_NewServiceInput_needs}, "only": {"ts": "only"}, "port": {"ts": "port"}, "provider": {"ts": "provider"}, "public": {"ts": "public"}, "region": {"ts": "region"}, "repo": {"ts": "repo"}, "source": {"ts": "source"}, "target": {"ts": "target"}}, "required": ["name", "source"]};
