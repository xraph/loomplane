import type { Codec } from '../codec-runtime';
import { codec_EntrySummary_tasks } from './EntrySummary.tasks';
import { codec_EntrySummary_tested_on } from './EntrySummary.tested_on';
import { codec_LicenseInfo } from './LicenseInfo';

export const codec_EntrySummary: Codec = {"kind": "object", "fields": {"context_length": {"ts": "contextLength"}, "cpu": {"ts": "cpu"}, "family": {"ts": "family"}, "fits_here": {"ts": "fitsHere"}, "gated": {"ts": "gated"}, "id": {"ts": "id"}, "license": {"ts": "license", "codec": () => codec_LicenseInfo}, "min_gpu_memory_gib": {"ts": "minGpuMemoryGib"}, "parameters": {"ts": "parameters"}, "publisher": {"ts": "publisher"}, "summary": {"ts": "summary"}, "tasks": {"ts": "tasks", "codec": () => codec_EntrySummary_tasks}, "tested_on": {"ts": "testedOn", "codec": () => codec_EntrySummary_tested_on}, "title": {"ts": "title"}, "verification": {"ts": "verification"}}, "required": ["context_length", "cpu", "family", "gated", "id", "license", "min_gpu_memory_gib", "parameters", "publisher", "summary", "tasks", "tested_on", "title", "verification"]};
