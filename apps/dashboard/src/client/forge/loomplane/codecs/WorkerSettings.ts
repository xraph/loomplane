import type { Codec } from '../codec-runtime';
import { codec_WorkerSettings_compatibility_flags } from './WorkerSettings.compatibility_flags';
import { codec_WorkerSettings_durable_objects } from './WorkerSettings.durable_objects';

export const codec_WorkerSettings: Codec = {"kind": "object", "fields": {"compatibility_date": {"ts": "compatibilityDate"}, "compatibility_flags": {"ts": "compatibilityFlags", "codec": () => codec_WorkerSettings_compatibility_flags}, "durable_objects": {"ts": "durableObjects", "codec": () => codec_WorkerSettings_durable_objects}, "main": {"ts": "main"}}, "required": ["compatibility_date", "main"]};
