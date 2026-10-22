import type { Codec } from '../codec-runtime';
import { codec_Port } from './Port';

export const codec_ResolvedComponent_ports: Codec = {"kind": "array", "items": () => codec_Port};
