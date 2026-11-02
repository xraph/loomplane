import type { Codec } from '../codec-runtime';
import { codec_Accelerator } from './Accelerator';
import { codec_ServerSize_architectures } from './ServerSize.architectures';
import { codec_ServerSize_regions } from './ServerSize.regions';

export const codec_ServerSize: Codec = {"kind": "object", "fields": {"accelerator": {"ts": "accelerator", "codec": () => codec_Accelerator}, "architectures": {"ts": "architectures", "codec": () => codec_ServerSize_architectures}, "cpus": {"ts": "cpus"}, "disk_gb": {"ts": "diskGb"}, "hourly_price_known": {"ts": "hourlyPriceKnown"}, "hourly_usd": {"ts": "hourlyUsd"}, "id": {"ts": "id"}, "memory_mib": {"ts": "memoryMib"}, "monthly_usd": {"ts": "monthlyUsd"}, "regions": {"ts": "regions", "codec": () => codec_ServerSize_regions}}, "required": ["cpus", "disk_gb", "hourly_usd", "id", "memory_mib", "monthly_usd", "regions"]};
