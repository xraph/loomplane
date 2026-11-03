import type { Codec } from '../codec-runtime';
import { codec_NodeUpdateBody_endpoints } from './NodeUpdateBody.endpoints';
import { codec_NodeUpdateBody_labels } from './NodeUpdateBody.labels';

export const codec_NodeUpdateBody: Codec = {"kind": "object", "fields": {"endpoints": {"ts": "endpoints", "codec": () => codec_NodeUpdateBody_endpoints}, "ingress_mode": {"ts": "ingressMode"}, "labels": {"ts": "labels", "codec": () => codec_NodeUpdateBody_labels}, "name": {"ts": "name"}}};
