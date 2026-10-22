import type { Codec } from '../codec-runtime';
import { codec_CatalogconnectEndpoint } from './CatalogconnectEndpoint';

export const codec_EndpointList_endpoints: Codec = {"kind": "array", "items": () => codec_CatalogconnectEndpoint};
