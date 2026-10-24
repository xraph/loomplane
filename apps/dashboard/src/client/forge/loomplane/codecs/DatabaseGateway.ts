import type { Codec } from '../codec-runtime';
import { codec_DatabaseGateway_cloud_tags } from './DatabaseGateway.cloud_tags';
import { codec_DatabaseGateway_environments } from './DatabaseGateway.environments';

export const codec_DatabaseGateway: Codec = {"kind": "object", "fields": {"cloud_tags": {"ts": "cloudTags", "codec": () => codec_DatabaseGateway_cloud_tags}, "environments": {"ts": "environments", "codec": () => codec_DatabaseGateway_environments}, "provider": {"ts": "provider"}, "region": {"ts": "region"}}};
