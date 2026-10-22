import type { Codec } from '../codec-runtime';
import { codec_ComponentConfig } from './ComponentConfig';
import { codec_CompositionConfig } from './CompositionConfig';
import { codec_ManagedDatabaseConfig } from './ManagedDatabaseConfig';
import { codec_Recipe } from './Recipe';
import { codec_ResourceConfig_routable_ports } from './ResourceConfig.routable_ports';
import { codec_ResourceConfig_target_settings } from './ResourceConfig.target_settings';

export const codec_ResourceConfig: Codec = {"kind": "object", "fields": {"catalog": {"ts": "catalog", "codec": () => codec_Recipe}, "component": {"ts": "component", "codec": () => codec_ComponentConfig}, "composition": {"ts": "composition", "codec": () => codec_CompositionConfig}, "kind": {"ts": "kind"}, "managed": {"ts": "managed", "codec": () => codec_ManagedDatabaseConfig}, "provider": {"ts": "provider"}, "region": {"ts": "region"}, "routable_ports": {"ts": "routablePorts", "codec": () => codec_ResourceConfig_routable_ports}, "target": {"ts": "target"}, "target_settings": {"ts": "targetSettings", "codec": () => codec_ResourceConfig_target_settings}, "version": {"ts": "version"}}, "required": ["kind", "provider"]};
