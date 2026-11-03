import type { Codec } from '../codec-runtime';
import { codec_GitLabStatus_connections } from './GitLabStatus.connections';
import { codec_GitLabStatus_hosts } from './GitLabStatus.hosts';

export const codec_GitLabStatus: Codec = {"kind": "object", "fields": {"callback_url": {"ts": "callbackUrl"}, "can_connect": {"ts": "canConnect"}, "connections": {"ts": "connections", "codec": () => codec_GitLabStatus_connections}, "hosts": {"ts": "hosts", "codec": () => codec_GitLabStatus_hosts}, "organization_hosts": {"ts": "organizationHosts"}}, "required": ["callback_url", "can_connect", "connections", "hosts", "organization_hosts"]};
