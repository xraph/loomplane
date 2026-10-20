import type { Codec } from '../codec-runtime';
import { codec_WebhookEndpoint } from './WebhookEndpoint';

export const codec_WebhookEndpointSecret: Codec = {"kind": "object", "fields": {"endpoint": {"ts": "endpoint", "codec": () => codec_WebhookEndpoint}, "secret": {"ts": "secret"}}, "required": ["endpoint", "secret"]};
