import type { Codec } from '../codec-runtime';
import { codec_SettingsInputBody_default_limits } from './SettingsInputBody.default_limits';
import { codec_SettingsInputBody_dns } from './SettingsInputBody.dns';
import { codec_SettingsInputBody_registry } from './SettingsInputBody.registry';

export const codec_SettingsInputBody: Codec = {"kind": "object", "fields": {"apex": {"ts": "apex"}, "attach": {"ts": "attach"}, "default_limits": {"ts": "defaultLimits", "codec": () => codec_SettingsInputBody_default_limits}, "dns": {"ts": "dns", "codec": () => codec_SettingsInputBody_dns}, "dns_account_id": {"ts": "dnsAccountId"}, "dns_team_id": {"ts": "dnsTeamId"}, "dns_token": {"ts": "dnsToken"}, "registry": {"ts": "registry", "codec": () => codec_SettingsInputBody_registry}, "registry_password": {"ts": "registryPassword"}, "registry_username": {"ts": "registryUsername"}}, "required": ["apex", "attach", "default_limits", "dns", "registry"]};
