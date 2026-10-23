import type { Codec } from '../codec-runtime';
import { codec_CompositionConfig_dependencies } from './CompositionConfig.dependencies';
import { codec_CompositionConfig_outputs } from './CompositionConfig.outputs';
import { codec_CompositionConfig_secrets } from './CompositionConfig.secrets';

export const codec_CompositionConfig: Codec = {"kind": "object", "fields": {"dependencies": {"ts": "dependencies", "codec": () => codec_CompositionConfig_dependencies}, "kind": {"ts": "kind"}, "outputs": {"ts": "outputs", "codec": () => codec_CompositionConfig_outputs}, "revision": {"ts": "revision"}, "secrets": {"ts": "secrets", "codec": () => codec_CompositionConfig_secrets}, "version": {"ts": "version"}}, "required": ["kind", "revision", "version"]};
