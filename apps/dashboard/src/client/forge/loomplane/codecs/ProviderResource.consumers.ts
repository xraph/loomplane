import type { Codec } from '../codec-runtime';
import { codec_ProviderResourceConsumer } from './ProviderResourceConsumer';

export const codec_ProviderResource_consumers: Codec = {"kind": "array", "items": () => codec_ProviderResourceConsumer};
