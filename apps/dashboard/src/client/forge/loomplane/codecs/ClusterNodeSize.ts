import type { Codec } from '../codec-runtime';
import { codec_ClusterNodeSize_regions } from './ClusterNodeSize.regions';

export const codec_ClusterNodeSize: Codec = {"kind": "object", "fields": {"cpus": {"ts": "cpus"}, "disk_gb": {"ts": "diskGb"}, "hourly_usd": {"ts": "hourlyUsd"}, "id": {"ts": "id"}, "memory_mib": {"ts": "memoryMib"}, "monthly_usd": {"ts": "monthlyUsd"}, "regions": {"ts": "regions", "codec": () => codec_ClusterNodeSize_regions}}, "required": ["cpus", "disk_gb", "id", "memory_mib", "regions"]};
