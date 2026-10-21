import type { Codec } from '../codec-runtime';
import { codec_ManagedDatabaseConfig } from './ManagedDatabaseConfig';
import { codec_Recipe } from './Recipe';
import { codec_ResourceConfig_target_settings } from './ResourceConfig.target_settings';

export const codec_ResourceConfig: Codec = {"kind": "object", "fields": {"catalog": {"ts": "catalog", "codec": () => codec_Recipe}, "kind": {"ts": "kind"}, "managed": {"ts": "managed", "codec": () => codec_ManagedDatabaseConfig}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "target": {"ts": "target"}, "target_settings": {"ts": "targetSettings", "codec": () => codec_ResourceConfig_target_settings}, "version": {"ts": "version"}}, "required": ["kind", "provider"]};
