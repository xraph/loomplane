import type { Codec } from '../codec-runtime';
import { codec_DatabaseGateway } from './DatabaseGateway';
import { codec_Defaults } from './Defaults';
import { codec_Federation } from './Federation';
import { codec_Held } from './Held';
import { codec_Ignore } from './Ignore';
import { codec_SaveInputBody_manifest_gateways } from './SaveInputBody.manifest.gateways';
import { codec_SaveInputBody_manifest_links } from './SaveInputBody.manifest.links';
import { codec_SaveInputBody_manifest_resources } from './SaveInputBody.manifest.resources';
import { codec_SaveInputBody_manifest_services } from './SaveInputBody.manifest.services';

export const codec_SaveInputBody_manifest: Codec = {"kind": "object", "fields": {"database_gateway": {"ts": "databaseGateway", "codec": () => codec_DatabaseGateway}, "defaults": {"ts": "defaults", "codec": () => codec_Defaults}, "federation": {"ts": "federation", "codec": () => codec_Federation}, "gateways": {"ts": "gateways", "codec": () => codec_SaveInputBody_manifest_gateways}, "held": {"ts": "held", "codec": () => codec_Held}, "ignore": {"ts": "ignore", "codec": () => codec_Ignore}, "links": {"ts": "links", "codec": () => codec_SaveInputBody_manifest_links}, "resources": {"ts": "resources", "codec": () => codec_SaveInputBody_manifest_resources}, "services": {"ts": "services", "codec": () => codec_SaveInputBody_manifest_services}, "version": {"ts": "version"}}, "required": ["version"]};
