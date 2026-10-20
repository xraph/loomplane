import type { Codec } from '../codec-runtime';
import { codec_DeliveryList_items } from './DeliveryList.items';

export const codec_DeliveryList: Codec = {"kind": "object", "fields": {"items": {"ts": "items", "codec": () => codec_DeliveryList_items}}, "required": ["items"]};
