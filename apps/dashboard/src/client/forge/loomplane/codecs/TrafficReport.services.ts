import type { Codec } from '../codec-runtime';
import { codec_ServiceTraffic } from './ServiceTraffic';

export const codec_TrafficReport_services: Codec = {"kind": "array", "items": () => codec_ServiceTraffic};
