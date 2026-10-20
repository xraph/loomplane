import type { Codec } from '../codec-runtime';
import { codec_WebhookEndpoint } from './WebhookEndpoint';

export const codec_WebhookEndpointList_items: Codec = {"kind": "array", "items": () => codec_WebhookEndpoint};
