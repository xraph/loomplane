import type { Codec } from '../codec-runtime';
import { codec_ConnectionsCreateInputBody_settings } from './ConnectionsCreateInputBody.settings';

export const codec_ConnectionsCreateInputBody: Codec = {"kind": "object", "fields": {"cloud": {"ts": "cloud"}, "credential": {"ts": "credential"}, "name": {"ts": "name"}, "registry_account_id": {"ts": "registryAccountId"}, "settings": {"ts": "settings", "codec": () => codec_ConnectionsCreateInputBody_settings}}, "required": ["cloud", "credential", "name"]};
