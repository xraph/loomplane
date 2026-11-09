import type { Codec } from '../codec-runtime';
import { codec_Compatibility } from './Compatibility';
import { codec_Manifest } from './Manifest';
import { codec_Template_config } from './Template_config';
import { codec_Template_jobs } from './Template.jobs';
import { codec_Template_parameters } from './Template.parameters';
import { codec_Template_profiles } from './Template.profiles';
import { codec_Template_required_capabilities } from './Template.required_capabilities';
import { codec_Template_resources } from './Template.resources';
import { codec_Template_secrets } from './Template.secrets';

export const codec_Template: Codec = {"kind": "object", "fields": {"compatibility": {"ts": "compatibility", "codec": () => codec_Compatibility}, "config": {"ts": "config", "codec": () => codec_Template_config}, "jobs": {"ts": "jobs", "codec": () => codec_Template_jobs}, "manifest": {"ts": "manifest", "codec": () => codec_Manifest}, "parameters": {"ts": "parameters", "codec": () => codec_Template_parameters}, "profiles": {"ts": "profiles", "codec": () => codec_Template_profiles}, "required_capabilities": {"ts": "requiredCapabilities", "codec": () => codec_Template_required_capabilities}, "resources": {"ts": "resources", "codec": () => codec_Template_resources}, "schema_version": {"ts": "schemaVersion"}, "secrets": {"ts": "secrets", "codec": () => codec_Template_secrets}}, "required": ["compatibility", "manifest", "profiles", "schema_version"]};
