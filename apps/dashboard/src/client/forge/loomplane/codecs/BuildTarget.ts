import type { Codec } from '../codec-runtime';
import { codec_BuildTarget_builder_settings } from './BuildTarget.builder_settings';
import { codec_BuildTarget_registry_settings } from './BuildTarget.registry_settings';

export const codec_BuildTarget: Codec = {"kind": "object", "fields": {"builder": {"ts": "builder"}, "builder_cloud": {"ts": "builderCloud"}, "builder_settings": {"ts": "builderSettings", "codec": () => codec_BuildTarget_builder_settings}, "image": {"ts": "image"}, "registry": {"ts": "registry"}, "registry_settings": {"ts": "registrySettings", "codec": () => codec_BuildTarget_registry_settings}}};
