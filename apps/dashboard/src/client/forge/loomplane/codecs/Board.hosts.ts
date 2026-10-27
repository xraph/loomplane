import type { Codec } from '../codec-runtime';
import { codec_HostCard } from './HostCard';

export const codec_Board_hosts: Codec = {"kind": "array", "items": () => codec_HostCard};
