import type { Codec } from '../codec-runtime';
import { codec_FleetEvent } from './FleetEvent';

export const codec_EventPage_items: Codec = {"kind": "array", "items": () => codec_FleetEvent};
