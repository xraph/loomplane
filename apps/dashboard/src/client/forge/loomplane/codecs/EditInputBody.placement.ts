import type { Codec } from '../codec-runtime';
import { codec_Build } from './Build';
import { codec_EditInputBody_placement_cloud_tags } from './EditInputBody.placement.cloud_tags';
import { codec_Endpoint } from './Endpoint';

export const codec_EditInputBody_placement: Codec = {"kind": "object", "fields": {"build": {"ts": "build", "codec": () => codec_Build}, "cloud_tags": {"ts": "cloudTags", "codec": () => codec_EditInputBody_placement_cloud_tags}, "cloud_target": {"ts": "cloudTarget"}, "enabled": {"ts": "enabled"}, "endpoint": {"ts": "endpoint", "codec": () => codec_Endpoint}, "environment": {"ts": "environment"}, "health_path": {"ts": "healthPath"}, "name": {"ts": "name"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "section": {"ts": "section"}, "target": {"ts": "target"}}, "required": ["name", "section"]};
