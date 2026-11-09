import type { Codec } from '../codec-runtime';
import { codec_PlatformDNSSettings } from './PlatformDNSSettings';
import { codec_PlatformLimits } from './PlatformLimits';
import { codec_RegistrySettings } from './RegistrySettings';

export const codec_SettingsView: Codec = {"kind": "object", "fields": {"apex": {"ts": "apex"}, "attach": {"ts": "attach"}, "default_limits": {"ts": "defaultLimits", "codec": () => codec_PlatformLimits}, "dns": {"ts": "dns", "codec": () => codec_PlatformDNSSettings}, "dns_credential_saved": {"ts": "dnsCredentialSaved"}, "registry": {"ts": "registry", "codec": () => codec_RegistrySettings}, "registry_credential_saved": {"ts": "registryCredentialSaved"}}, "required": ["apex", "attach", "default_limits", "dns", "dns_credential_saved", "registry", "registry_credential_saved"]};
