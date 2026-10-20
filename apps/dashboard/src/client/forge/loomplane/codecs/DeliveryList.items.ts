import type { Codec } from '../codec-runtime';
import { codec_Delivery } from './Delivery';

export const codec_DeliveryList_items: Codec = {"kind": "array", "items": () => codec_Delivery};
