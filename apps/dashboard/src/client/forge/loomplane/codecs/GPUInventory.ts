import type { Codec } from '../codec-runtime';
import { codec_GPUInventory_devices } from './GPUInventory.devices';
import { codec_GPUInventory_nodes } from './GPUInventory.nodes';

export const codec_GPUInventory: Codec = {"kind": "object", "fields": {"checked_at": {"ts": "checkedAt"}, "devices": {"ts": "devices", "codec": () => codec_GPUInventory_devices}, "message": {"ts": "message"}, "nodes": {"ts": "nodes", "codec": () => codec_GPUInventory_nodes}, "status": {"ts": "status"}}, "required": ["checked_at", "status"]};
