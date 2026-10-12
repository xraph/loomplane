// Generated codec runtime, with no table behind it.
//
// The same walk codecs.ts carries, rendered without the branch that resolves
// a string id, so importing it reaches no codec at all. fetch.ts imports this
// copy and receives the codec itself on the request config.

export type Codec =
  | { kind: 'object'; fields: Record<string, { ts: string; codec?: CodecRef | undefined }>; required?: string[]; values?: CodecRef }
  | { kind: 'array'; items?: CodecRef }
  | { kind: 'record'; values?: CodecRef }
  | { kind: 'union'; discriminator?: { wire: string; map: Record<string, CodecRef> }; members: CodecRef[] }
  | { kind: 'passthrough' };

/**
 * A codec, a thunk returning one, or an id to look up in CODECS.
 *
 * The thunk is not a style choice. The reference graph has cycles -- a
 * PredicateSpec holds an array of PredicateSpec -- and a direct object
 * reference between two modules makes each initialise in terms of the other,
 * so whichever evaluates second reads the first in its temporal dead zone and
 * throws at import time. A thunk defers that read to walk time.
 */
export type CodecRef = string | Codec | (() => Codec);

function codecFor(ref?: CodecRef): Codec | undefined {
  if (ref === undefined) {
    return undefined;
  }

  // A thunk, not the codec itself, because the reference graph has cycles:
  // a PredicateSpec holds an array of PredicateSpec. Written as a direct
  // object reference, the two module bindings initialise in terms of each
  // other and whichever evaluates second reads the first in its temporal dead
  // zone, which is a ReferenceError at import time rather than anything the
  // type system would have caught. Deferring the read to walk time removes the
  // cycle from module evaluation entirely.
  if (typeof ref === 'function') {
    return ref();
  }

  // An id, resolved against the table this runtime was rendered beside. The
  // split codec modules never take this path -- they carry references -- which
  // is what keeps a per-operation import chain clear of the table entirely.
  if (typeof ref === 'string') {
    return undefined;
  }

  return ref;
}

// setOwn assigns obj[key] = value via Object.defineProperty rather than
// bracket/dot assignment. This matters for exactly one key: "__proto__".
// obj[key] = value, when key is the literal string "__proto__", goes
// through the legacy Object.prototype.__proto__ ACCESSOR -- it sets obj's
// PROTOTYPE (silently ignoring the assignment entirely if value is not an
// object or null) instead of creating an own data property. A wire (or,
// under NamingPreserve/an override, client) field literally named
// "__proto__" would otherwise silently vanish from the walked result: not
// an error, just an object one property short of what the codec table
// claims it renamed. Object.defineProperty has no such special case for any
// key, "__proto__" included, so it is used unconditionally here rather than
// only when key happens to be "__proto__" -- one code path, not two.
function setOwn(obj: Record<string, unknown>, key: string, value: unknown): void {
  Object.defineProperty(obj, key, { value, writable: true, enumerable: true, configurable: true });
}

function walk(value: unknown, ref: CodecRef | undefined, toTS: boolean): unknown {
  const codec = codecFor(ref);
  if (!codec || value === null || value === undefined) {
    return value;
  }

  switch (codec.kind) {
    case 'object': {
      if (typeof value !== 'object' || Array.isArray(value)) {
        return value;
      }

      const src = value as Record<string, unknown>;
      const out: Record<string, unknown> = {};

      // Build the rename map in the requested direction. Decoding maps a
      // wire key to its ts name; encoding maps back.
      const rename = new Map<string, { to: string; codec?: CodecRef | undefined }>();
      for (const [wire, field] of Object.entries(codec.fields)) {
        if (toTS) {
          rename.set(wire, { to: field.ts, codec: field.codec });
        } else {
          rename.set(field.ts, { to: wire, codec: field.codec });
        }
      }

      for (const [key, val] of Object.entries(src)) {
        const mapped = rename.get(key);
        if (mapped) {
          setOwn(out, mapped.to, walk(val, mapped.codec, toTS));
        } else if (codec.values) {
          // Not a declared field, but this object also has a typed
          // additionalProperties value schema (a declared-Properties-AND-
          // additionalProperties schema, rendered as an intersection type):
          // the KEY is data and stays untouched, exactly like the 'record'
          // case below, but the VALUE still needs walking so its own
          // fields get renamed too.
          setOwn(out, key, walk(val, codec.values, toTS));
        } else {
          // Unknown key: pass through verbatim, name and value untouched.
          setOwn(out, key, val);
        }
      }

      return out;
    }

    case 'array': {
      if (!Array.isArray(value)) {
        return value;
      }

      return value.map((item) => walk(item, codec.items, toTS));
    }

    case 'record': {
      if (typeof value !== 'object' || Array.isArray(value)) {
        return value;
      }

      const src = value as Record<string, unknown>;
      const out: Record<string, unknown> = {};

      // Keys are data here, not field names — they are never renamed.
      for (const [key, val] of Object.entries(src)) {
        setOwn(out, key, walk(val, codec.values, toTS));
      }

      return out;
    }

    case 'union': {
      if (typeof value !== 'object' || Array.isArray(value)) {
        return value;
      }

      const src = value as Record<string, unknown>;

      if (codec.discriminator) {
        // codec.discriminator.wire is the WIRE name of the tag property
        // (e.g. "pet_kind"). When decoding (toTS), src IS wire-shaped, so
        // reading it directly is correct -- this is the pre-existing,
        // already-correct half, and is untouched below.
        //
        // When ENCODING (!toTS), src is TS-shaped (e.g. { petKind: 'dog' }):
        // reading src[codec.discriminator.wire] looks up a key that does not
        // exist on a TS-shaped object at all, so a naive single read was
        // always undefined and fell through to the passthrough below --
        // silently shipping the whole union unrenamed. (Fix-round-1 review,
        // CRITICAL.)
        //
        // A first fix tried ONE candidate: the ts name the FIRST member
        // (in declared order) that happens to declare this wire property
        // renders it as. That is correct only when every member agrees on
        // that name -- fix-round-2 review measured two ways it silently
        // breaks:
        //
        //   (a) members DISAGREE on the ts name (e.g. a schema-scoped
        //       FieldOverrides entry renaming only one member's rendering).
        //       The first-declared member's name wins the scan regardless
        //       of which member the payload actually is, so a payload
        //       matching a LATER member under ITS OWN name never resolves;
        //   (b) NO member declares the property at all (a lenient spec --
        //       strict OpenAPI requires it on every member, but nothing
        //       here enforces that). The scan finds nothing, falls back to
        //       the bare wire name, which is virtually never present on a
        //       TS-shaped payload since the property isn't even part of any
        //       member's declared TypeScript type.
        //
        // Fixed by trying every DISTINCT ts name any member declares for
        // this wire property, in declared order, plus the wire name itself
        // as a final fallback (covers a lenient spec where a member
        // legitimately preserves the wire name, e.g. under
        // NamingPreserve/an override that reproduces it exactly). A
        // candidate is accepted only when BOTH it exists as a string key on
        // src AND codec.discriminator.map resolves that string to a real
        // member -- so trying more than one candidate can never invent a
        // match a single-candidate read wouldn't also have found under the
        // right name. If two DIFFERENT candidates each resolve to a
        // DIFFERENT member, the payload is genuinely ambiguous (which
        // member did the caller actually mean?) and this passes through
        // rather than guessing, exactly like the undiscriminated
        // structural match below already does for its own ambiguous case.
        // codecTable.checkDiscriminatorTSNameAgreement (codecs.go) warns at
        // generation time when members disagree on, or entirely omit, this
        // property -- both are real spec smells the caller should see.
        let memberID: CodecRef | undefined;

        if (toTS) {
          const tag = src[codec.discriminator.wire];
          memberID = typeof tag === 'string' ? codec.discriminator.map[tag] : undefined;
        } else {
          const candidates: string[] = [];
          const seenCandidates = new Set<string>();
          const addCandidate = (key: string) => {
            if (!seenCandidates.has(key)) {
              seenCandidates.add(key);
              candidates.push(key);
            }
          };

          for (const mID of codec.members ?? []) {
            const m = codecFor(mID);
            const declared = m && m.kind === 'object' ? m.fields[codec.discriminator.wire] : undefined;
            if (declared) {
              addCandidate(declared.ts);
            }
          }
          addCandidate(codec.discriminator.wire);

          let ambiguous = false;

          for (const key of candidates) {
            const candidateTag = src[key];
            if (typeof candidateTag !== 'string') {
              continue;
            }

            const candidateMemberID = codec.discriminator.map[candidateTag];
            if (!candidateMemberID) {
              continue;
            }

            if (memberID === undefined) {
              memberID = candidateMemberID;
            } else if (memberID !== candidateMemberID) {
              ambiguous = true;
            }
          }

          if (ambiguous) {
            memberID = undefined;
          }
        }

        if (!memberID) {
          return value;
        }

        return walk(value, memberID, toTS);
      }

      // No discriminator: try each member in the order it was declared,
      // taking the first whose required fields are ALL present on the
      // value. A member that is evidence-free -- not an 'object' at all, or
      // an object with no required fields -- is SKIPPED, not treated as a
      // vacuous match: an empty required list is satisfied by every object
      // trivially, which would turn that member into an unconditional
      // catch-all rather than a real structural test, defeating "never a
      // best-effort guess" for the common case where a schema simply has no
      // required fields declared (the OpenAPI default). If every member is
      // skipped, fall through to the same passthrough a genuine mismatch
      // gets -- generation-time warns when a member has no evidence to
      // offer (see unionEntry), so this is never a silent surprise.
      for (const memberID of codec.members ?? []) {
        const member = codecFor(memberID);
        if (!member || member.kind !== 'object') {
          continue;
        }

        const required = member.required;
        if (!required || required.length === 0) {
          continue;
        }

        // 'required' lists WIRE names (see requiredWireFields). When
        // decoding, 'src' is wire-shaped, so those names are tested
        // directly -- the pre-existing, already-correct half. When
        // ENCODING, 'src' is TS-shaped: testing a wire name like
        // "bark_volume" against it always failed, silently, because that
        // key never exists on a TS-shaped object -- the same class of bug
        // as the discriminator case above, just for the undiscriminated
        // structural match. (Fix-round-1 review, CRITICAL.) Fixed by
        // mapping each required wire name through this SAME member's own
        // 'fields' table to the ts name it renders as before testing
        // presence on 'src'.
        const keys = toTS ? required : required.map((wireKey) => member.fields[wireKey]?.ts ?? wireKey);

        if (keys.every((key) => Object.prototype.hasOwnProperty.call(src, key))) {
          return walk(value, memberID, toTS);
        }
      }

      // No member matched: pass through verbatim rather than guess.
      return value;
    }

    default:
      return value;
  }
}

/** Convert a wire payload into its TypeScript shape. */
export function decode<T>(value: unknown, codecRef?: CodecRef): T {
  return walk(value, codecRef, true) as T;
}

/** Convert a TypeScript value into its wire shape. */
export function encode(value: unknown, codecRef?: CodecRef): unknown {
  return walk(value, codecRef, false);
}
