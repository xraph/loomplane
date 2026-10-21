import { mutation } from '@forge-go/client-core';
import { op_clouds_create } from '../ops/clouds.create';
import type { Profile } from '../types';

export const useCloudsCreate = /*#__PURE__*/ mutation<Profile, Profile>(op_clouds_create);
