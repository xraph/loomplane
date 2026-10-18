import type { Codec } from '../codec-runtime';
import { codec_Defaults } from './Defaults';
import { codec_Federation } from './Federation';
import { codec_Held } from './Held';
import { codec_Ignore } from './Ignore';
import { codec_Manifest_gateways } from './Manifest.gateways';
import { codec_Manifest_links } from './Manifest.links';
import { codec_Manifest_resources } from './Manifest.resources';
import { codec_Manifest_services } from './Manifest.services';

export const codec_Manifest: Codec = {"kind": "object", "fields": {"defaults": {"ts": "defaults", "codec": () => codec_Defaults}, "federation": {"ts": "federation", "codec": () => codec_Federation}, "gateways": {"ts": "gateways", "codec": () => codec_Manifest_gateways}, "held": {"ts": "held", "codec": () => codec_Held}, "ignore": {"ts": "ignore", "codec": () => codec_Ignore}, "links": {"ts": "links", "codec": () => codec_Manifest_links}, "resources": {"ts": "resources", "codec": () => codec_Manifest_resources}, "services": {"ts": "services", "codec": () => codec_Manifest_services}, "version": {"ts": "version"}}, "required": ["version"]};
