import type { Codec } from '../codec-runtime';
import { codec_Defaults } from './Defaults';
import { codec_Held } from './Held';
import { codec_Ignore } from './Ignore';
import { codec_SaveInputBody_manifest_gateways } from './SaveInputBody.manifest.gateways';
import { codec_SaveInputBody_manifest_resources } from './SaveInputBody.manifest.resources';
import { codec_SaveInputBody_manifest_services } from './SaveInputBody.manifest.services';

export const codec_SaveInputBody_manifest: Codec = {"kind": "object", "fields": {"defaults": {"ts": "defaults", "codec": () => codec_Defaults}, "gateways": {"ts": "gateways", "codec": () => codec_SaveInputBody_manifest_gateways}, "held": {"ts": "held", "codec": () => codec_Held}, "ignore": {"ts": "ignore", "codec": () => codec_Ignore}, "resources": {"ts": "resources", "codec": () => codec_SaveInputBody_manifest_resources}, "services": {"ts": "services", "codec": () => codec_SaveInputBody_manifest_services}, "version": {"ts": "version"}}, "required": ["defaults", "ignore", "version"]};
