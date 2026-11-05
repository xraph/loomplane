import type { Codec } from '../codec-runtime';
import { codec_PlacementGroupReview_members } from './PlacementGroupReview.members';

export const codec_PlacementGroupReview: Codec = {"kind": "object", "fields": {"generation": {"ts": "generation"}, "group_id": {"ts": "groupId"}, "hostname": {"ts": "hostname"}, "image": {"ts": "image"}, "members": {"ts": "members", "codec": () => codec_PlacementGroupReview_members}, "min_ready": {"ts": "minReady"}, "name": {"ts": "name"}, "revision": {"ts": "revision"}, "routing_mode": {"ts": "routingMode"}, "service": {"ts": "service"}}, "required": ["generation", "group_id", "hostname", "image", "members", "min_ready", "name", "revision", "routing_mode", "service"]};
