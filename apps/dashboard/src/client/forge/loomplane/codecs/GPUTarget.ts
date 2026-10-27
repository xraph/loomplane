import type { Codec } from '../codec-runtime';
import { codec_Accelerator } from './Accelerator';
import { codec_GPUTarget_regions } from './GPUTarget.regions';

export const codec_GPUTarget: Codec = {"kind": "object", "fields": {"accelerator": {"ts": "accelerator", "codec": () => codec_Accelerator}, "cloud": {"ts": "cloud"}, "code": {"ts": "code"}, "connection_id": {"ts": "connectionId"}, "fits": {"ts": "fits"}, "free": {"ts": "free"}, "hourly_price_known": {"ts": "hourlyPriceKnown"}, "hourly_usd": {"ts": "hourlyUsd"}, "message": {"ts": "message"}, "name": {"ts": "name"}, "price_estimate": {"ts": "priceEstimate"}, "regions": {"ts": "regions", "codec": () => codec_GPUTarget_regions}, "size_id": {"ts": "sizeId"}, "source": {"ts": "source"}}, "required": ["accelerator", "cloud", "connection_id", "fits", "free", "hourly_price_known", "hourly_usd", "name", "price_estimate", "source"]};
