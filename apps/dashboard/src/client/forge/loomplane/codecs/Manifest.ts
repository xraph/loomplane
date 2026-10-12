import type { Codec } from '../codec-runtime';
import { codec_Defaults } from './Defaults';
import { codec_Ignore } from './Ignore';
import { codec_Manifest_gateways } from './Manifest.gateways';
import { codec_Manifest_resources } from './Manifest.resources';
import { codec_Manifest_services } from './Manifest.services';

export const codec_Manifest: Codec = {"kind": "object", "fields": {"defaults": {"ts": "defaults", "codec": () => codec_Defaults}, "gateways": {"ts": "gateways", "codec": () => codec_Manifest_gateways}, "ignore": {"ts": "ignore", "codec": () => codec_Ignore}, "resources": {"ts": "resources", "codec": () => codec_Manifest_resources}, "services": {"ts": "services", "codec": () => codec_Manifest_services}, "version": {"ts": "version"}}, "required": ["defaults", "ignore", "version"]};
