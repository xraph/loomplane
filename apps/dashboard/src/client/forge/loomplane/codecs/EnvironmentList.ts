import type { Codec } from '../codec-runtime';
import { codec_EnvironmentList_environments } from './EnvironmentList.environments';

export const codec_EnvironmentList: Codec = {"kind": "object", "fields": {"environments": {"ts": "environments", "codec": () => codec_EnvironmentList_environments}}, "required": ["environments"]};
