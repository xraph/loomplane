import type { Codec } from '../codec-runtime';
import { codec_Build } from './Build';
import { codec_CloudSQLApplicationNetwork } from './CloudSQLApplicationNetwork';
import { codec_EditInputBody_placement_cloud_tags } from './EditInputBody.placement.cloud_tags';
import { codec_ManifestEndpoint } from './ManifestEndpoint';
import { codec_Scale } from './Scale';
import { codec_SizeEdit } from './SizeEdit';

export const codec_EditInputBody_placement: Codec = {"kind": "object", "fields": {"build": {"ts": "build", "codec": () => codec_Build}, "cloud_sql_network": {"ts": "cloudSqlNetwork", "codec": () => codec_CloudSQLApplicationNetwork}, "cloud_tags": {"ts": "cloudTags", "codec": () => codec_EditInputBody_placement_cloud_tags}, "cloud_target": {"ts": "cloudTarget"}, "enabled": {"ts": "enabled"}, "endpoint": {"ts": "endpoint", "codec": () => codec_ManifestEndpoint}, "environment": {"ts": "environment"}, "health_path": {"ts": "healthPath"}, "name": {"ts": "name"}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "scale": {"ts": "scale", "codec": () => codec_Scale}, "section": {"ts": "section"}, "size": {"ts": "size", "codec": () => codec_SizeEdit}, "target": {"ts": "target"}}, "required": ["name", "section"]};
