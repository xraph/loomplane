import type { Codec } from '../codec-runtime';
import { codec_ManagedRegistryView_backups } from './ManagedRegistryView.backups';

export const codec_ManagedRegistryView: Codec = {"kind": "object", "fields": {"action": {"ts": "action"}, "backups": {"ts": "backups", "codec": () => codec_ManagedRegistryView_backups}, "connection_id": {"ts": "connectionId"}, "dns_ready": {"ts": "dnsReady"}, "generation": {"ts": "generation"}, "hostname": {"ts": "hostname"}, "message": {"ts": "message"}, "phase": {"ts": "phase"}, "status": {"ts": "status"}}, "required": ["action", "backups", "connection_id", "dns_ready", "generation", "hostname", "message", "phase", "status"]};
