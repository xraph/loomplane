import type { Codec } from '../codec-runtime';
import { codec_ClusterMaintenance } from './ClusterMaintenance';
import { codec_ClusterProviderOptions } from './ClusterProviderOptions';
import { codec_ClusterUpdate_field_mask } from './ClusterUpdate.field_mask';

export const codec_ClusterUpdate: Codec = {"kind": "object", "fields": {"field_mask": {"ts": "fieldMask", "codec": () => codec_ClusterUpdate_field_mask}, "ha": {"ts": "ha"}, "maintenance": {"ts": "maintenance", "codec": () => codec_ClusterMaintenance}, "provider_options": {"ts": "providerOptions", "codec": () => codec_ClusterProviderOptions}}, "required": ["field_mask", "ha", "maintenance"]};
