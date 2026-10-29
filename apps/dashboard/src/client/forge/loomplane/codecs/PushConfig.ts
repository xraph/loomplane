import type { Codec } from '../codec-runtime';

export const codec_PushConfig: Codec = {"kind": "object", "fields": {"enabled": {"ts": "enabled"}, "gateway_url": {"ts": "gatewayUrl"}, "key_id": {"ts": "keyId"}, "server_key": {"ts": "serverKey"}}, "required": ["enabled", "gateway_url", "key_id", "server_key"]};
