import type { Codec } from '../codec-runtime';
import { codec_DigitalOceanRegistryNames_names } from './DigitalOceanRegistryNames.names';

export const codec_DigitalOceanRegistryNames: Codec = {"kind": "object", "fields": {"names": {"ts": "names", "codec": () => codec_DigitalOceanRegistryNames_names}}, "required": ["names"]};
