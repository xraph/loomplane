import type { Codec } from '../codec-runtime';
import { codec_NodeJoinBody_inventory_endpoints } from './NodeJoinBody.inventory.endpoints';
import { codec_NodeJoinBody_inventory_gpus } from './NodeJoinBody.inventory.gpus';
import { codec_NodeJoinBody_inventory_public_addresses } from './NodeJoinBody.inventory.public_addresses';
import { codec_Runtime } from './Runtime';

export const codec_NodeJoinBody_inventory: Codec = {"kind": "object", "fields": {"arch": {"ts": "arch"}, "cpus": {"ts": "cpus"}, "disk_gib": {"ts": "diskGib"}, "docker": {"ts": "docker", "codec": () => codec_Runtime}, "endpoints": {"ts": "endpoints", "codec": () => codec_NodeJoinBody_inventory_endpoints}, "gpus": {"ts": "gpus", "codec": () => codec_NodeJoinBody_inventory_gpus}, "hostname": {"ts": "hostname"}, "kernel": {"ts": "kernel"}, "memory_mib": {"ts": "memoryMib"}, "os": {"ts": "os"}, "podman": {"ts": "podman", "codec": () => codec_Runtime}, "ports_80_443": {"ts": "ports80443"}, "public_addresses": {"ts": "publicAddresses", "codec": () => codec_NodeJoinBody_inventory_public_addresses}}, "required": ["arch", "cpus", "disk_gib", "kernel", "memory_mib", "os"]};
