import type { Codec } from '../codec-runtime';
import { codec_Inventory_endpoints } from './Inventory.endpoints';
import { codec_Inventory_gpus } from './Inventory.gpus';
import { codec_Inventory_public_addresses } from './Inventory.public_addresses';
import { codec_Runtime } from './Runtime';

export const codec_Inventory: Codec = {"kind": "object", "fields": {"arch": {"ts": "arch"}, "cpus": {"ts": "cpus"}, "disk_gib": {"ts": "diskGib"}, "docker": {"ts": "docker", "codec": () => codec_Runtime}, "endpoints": {"ts": "endpoints", "codec": () => codec_Inventory_endpoints}, "gpus": {"ts": "gpus", "codec": () => codec_Inventory_gpus}, "hostname": {"ts": "hostname"}, "kernel": {"ts": "kernel"}, "memory_mib": {"ts": "memoryMib"}, "os": {"ts": "os"}, "podman": {"ts": "podman", "codec": () => codec_Runtime}, "ports_80_443": {"ts": "ports80443"}, "public_addresses": {"ts": "publicAddresses", "codec": () => codec_Inventory_public_addresses}}, "required": ["arch", "cpus", "disk_gib", "kernel", "memory_mib", "os"]};
