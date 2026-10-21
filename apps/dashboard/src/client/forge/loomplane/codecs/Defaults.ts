import type { Codec } from '../codec-runtime';
import { codec_Defaults_cloud_tags } from './Defaults.cloud_tags';

export const codec_Defaults: Codec = {"kind": "object", "fields": {"cloud_tags": {"ts": "cloudTags", "codec": () => codec_Defaults_cloud_tags}, "provider": {"ts": "provider"}, "region": {"ts": "region"}}};
