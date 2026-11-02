import type { Codec } from '../codec-runtime';
import { codec_FarpConfig_metadata } from './FarpConfig.metadata';

export const codec_FarpConfig: Codec = {"kind": "object", "fields": {"gateway": {"ts": "gateway"}, "metadata": {"ts": "metadata", "codec": () => codec_FarpConfig_metadata}, "port": {"ts": "port"}}, "required": ["gateway", "port"]};
