import type { Codec } from '../codec-runtime';
import { codec_WebhookEndpointList_items } from './WebhookEndpointList.items';

export const codec_WebhookEndpointList: Codec = {"kind": "object", "fields": {"items": {"ts": "items", "codec": () => codec_WebhookEndpointList_items}}, "required": ["items"]};
