import type { Codec } from '../codec-runtime';
import { codec_AttachDefaults_blocked_path_prefixes } from './AttachDefaults.blocked_path_prefixes';

export const codec_AttachDefaults: Codec = {"kind": "object", "fields": {"blocked_path_prefixes": {"ts": "blockedPathPrefixes", "codec": () => codec_AttachDefaults_blocked_path_prefixes}, "cloud_id": {"ts": "cloudId"}, "environment_id": {"ts": "environmentId"}, "port": {"ts": "port"}, "service": {"ts": "service"}}, "required": ["blocked_path_prefixes", "environment_id", "port", "service"]};
