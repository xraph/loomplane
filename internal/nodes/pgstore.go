package nodes

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/xraph/grove/drivers/pgdriver"

	"github.com/xraph/loomplane/pkg/nodeproto"
)

// PgStore is the Postgres Store.
type PgStore struct{ db *pgdriver.PgDB }

// NewPgStore returns a store over db.
func NewPgStore(db *pgdriver.PgDB) *PgStore { return &PgStore{db: db} }

var _ Store = (*PgStore)(nil)

const nodeColumns = `id,org_id,name,public_key,key_fingerprint,identity_generation,status,endpoints,ingress_mode,role,edge_scope,labels,version,protocol_version,inventory,last_seen_at,joined_by,joined_by_key_id,revoked_at,revoked_by,created_at,updated_at`

type scanner interface{ Scan(...any) error }

func storeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		switch pe.Code {
		case "23505":
			if pe.ConstraintName == "loomplane_nodes_live_name" {
				return ErrNameTaken
			}
		case "23503":
			return ErrNotFound
		}
	}
	return err
}

func scanNode(row scanner) (*Node, error) {
	var n Node
	var status string
	var endpoints, labels, inventory []byte
	if err := row.Scan(&n.ID, &n.OrgID, &n.Name, &n.PublicKey, &n.KeyFingerprint, &n.IdentityGeneration, &status, &endpoints, &n.IngressMode, &n.Role, &n.EdgeScope, &labels, &n.Version, &n.ProtocolVersion, &inventory, &n.LastSeenAt, &n.JoinedBy, &n.JoinedByKeyID, &n.RevokedAt, &n.RevokedBy, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return nil, storeError(err)
	}
	n.Status = Status(status)
	if err := json.Unmarshal(endpoints, &n.Endpoints); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(labels, &n.Labels); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(inventory, &n.Inventory); err != nil {
		return nil, err
	}
	n.CreatedAt, n.UpdatedAt = n.CreatedAt.UTC(), n.UpdatedAt.UTC()
	n.LastSeenAt, n.RevokedAt = utc(n.LastSeenAt), utc(n.RevokedAt)
	return &n, nil
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}

// encode returns the jsonb columns of n. Nil collections become empty ones.
func encode(n *Node) (endpoints, labels, inventory []byte, err error) {
	eps := n.Endpoints
	if eps == nil {
		eps = []nodeproto.Endpoint{}
	}
	lbl := n.Labels
	if lbl == nil {
		lbl = map[string]string{}
	}
	if endpoints, err = json.Marshal(eps); err != nil {
		return nil, nil, nil, err
	}
	if labels, err = json.Marshal(lbl); err != nil {
		return nil, nil, nil, err
	}
	inventory, err = json.Marshal(n.Inventory)
	return endpoints, labels, inventory, err
}

// Create inserts n. It returns ErrNameTaken when a live node of the org has
// the name.
func (s *PgStore) Create(ctx context.Context, n *Node) error {
	if n.IdentityGeneration == 0 {
		n.IdentityGeneration = 1
	}
	if now := time.Now().UTC(); n.CreatedAt.IsZero() || n.UpdatedAt.IsZero() {
		if n.CreatedAt.IsZero() {
			n.CreatedAt = now
		}
		if n.UpdatedAt.IsZero() {
			n.UpdatedAt = now
		}
	}
	endpoints, labels, inventory, err := encode(n)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(ctx, `INSERT INTO loomplane_nodes(`+nodeColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		n.ID, n.OrgID, n.Name, n.PublicKey, n.KeyFingerprint, n.IdentityGeneration, string(n.Status), endpoints, n.IngressMode, n.Role, n.EdgeScope, labels, n.Version, n.ProtocolVersion, inventory, n.LastSeenAt, n.JoinedBy, n.JoinedByKeyID, n.RevokedAt, n.RevokedBy, n.CreatedAt, n.UpdatedAt)
	return storeError(err)
}

// Get returns a node of org.
func (s *PgStore) Get(ctx context.Context, org, id string) (*Node, error) {
	return scanNode(s.db.QueryRow(ctx, `SELECT `+nodeColumns+` FROM loomplane_nodes WHERE org_id=$1 AND id=$2`, org, id))
}

// GetByID returns a node without an org, for the connection path that only
// holds the id from a verified key.
func (s *PgStore) GetByID(ctx context.Context, id string) (*Node, error) {
	return scanNode(s.db.QueryRow(ctx, `SELECT `+nodeColumns+` FROM loomplane_nodes WHERE id=$1`, id))
}

func (s *PgStore) list(ctx context.Context, query string, args ...any) ([]Node, error) {
	rows, err := s.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Node{}
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

// List returns the org's nodes, oldest first.
func (s *PgStore) List(ctx context.Context, org string) ([]Node, error) {
	return s.list(ctx, `SELECT `+nodeColumns+` FROM loomplane_nodes WHERE org_id=$1 ORDER BY created_at,id`, org)
}

// ListJoinedByKey returns the org's nodes that joined with the key.
func (s *PgStore) ListJoinedByKey(ctx context.Context, org, keyID string) ([]Node, error) {
	return s.list(ctx, `SELECT `+nodeColumns+` FROM loomplane_nodes WHERE org_id=$1 AND joined_by_key_id=$2 ORDER BY created_at,id`, org, keyID)
}

// Update saves everything but the heartbeat columns, the org and the creation
// time. Revocation is final: a revoked node stays revoked, with its original
// time and revoker, whatever the caller's copy says.
func (s *PgStore) Update(ctx context.Context, n *Node) error {
	endpoints, labels, inventory, err := encode(n)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE loomplane_nodes SET name=$3,public_key=$4,key_fingerprint=$5,identity_generation=$6,status=CASE WHEN COALESCE(loomplane_nodes.revoked_at,$18::timestamptz) IS NOT NULL THEN 'revoked' ELSE $7 END,endpoints=$8,ingress_mode=$9,role=$10,edge_scope=$11,labels=$12,version=$13,protocol_version=$14,inventory=$15,joined_by=$16,joined_by_key_id=$17,revoked_at=COALESCE(loomplane_nodes.revoked_at,$18::timestamptz),revoked_by=CASE WHEN loomplane_nodes.revoked_at IS NOT NULL THEN loomplane_nodes.revoked_by ELSE $19 END,updated_at=now() WHERE org_id=$1 AND id=$2`,
		n.OrgID, n.ID, n.Name, n.PublicKey, n.KeyFingerprint, n.IdentityGeneration, string(n.Status), endpoints, n.IngressMode, n.Role, n.EdgeScope, labels, n.Version, n.ProtocolVersion, inventory, n.JoinedBy, n.JoinedByKeyID, n.RevokedAt, n.RevokedBy)
	return affected(res, err)
}

// UpdateSettings writes the settings u sets, and nothing else, on a node of
// org that is not revoked. It leaves status, keys and sessions alone, so a
// caller holding a stale copy cannot undo them. A non-nil Endpoints must be
// a subset of the stored set, checked in the same statement.
func (s *PgStore) UpdateSettings(ctx context.Context, org, id string, u SettingsUpdate) (*Node, error) {
	var name, ingress, labels, endpoints *string
	name, ingress = u.Name, u.IngressMode
	if u.Labels != nil {
		b, err := json.Marshal(u.Labels)
		if err != nil {
			return nil, err
		}
		v := string(b)
		labels = &v
	}
	if u.Endpoints != nil {
		b, err := json.Marshal(u.Endpoints)
		if err != nil {
			return nil, err
		}
		v := string(b)
		endpoints = &v
	}
	n, err := scanNode(s.db.QueryRow(ctx, `UPDATE loomplane_nodes SET name=COALESCE($3,name),labels=COALESCE($4::jsonb,labels),ingress_mode=COALESCE($5,ingress_mode),endpoints=COALESCE($6::jsonb,endpoints),updated_at=now() WHERE org_id=$1 AND id=$2 AND revoked_at IS NULL AND ($6::jsonb IS NULL OR $6::jsonb <@ endpoints) RETURNING `+nodeColumns,
		org, id, name, labels, ingress, endpoints))
	if !errors.Is(err, ErrNotFound) {
		return n, err
	}
	// No row changed: either there is no live node, or the new endpoints widen.
	cur, gerr := s.Get(ctx, org, id)
	if gerr == nil && cur.RevokedAt == nil && endpoints != nil {
		return nil, ErrWidenRemotely
	}
	return nil, ErrNotFound
}

func affected(res interface{ RowsAffected() (int64, error) }, err error) error {
	if err != nil {
		return storeError(err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Touch records a heartbeat. It writes last_seen_at and usage only: the event
// trigger leaves both out of its payload, so a heartbeat appends no event.
func (s *PgStore) Touch(ctx context.Context, id string, at time.Time, usage nodeproto.Usage) error {
	raw, err := json.Marshal(usage)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE loomplane_nodes SET last_seen_at=$2,usage=$3 WHERE id=$1`, id, at, raw)
	return affected(res, err)
}

// SetStatus moves a node to st. Once a node is revoked only 'revoked' is
// accepted.
func (s *PgStore) SetStatus(ctx context.Context, id string, st Status) error {
	res, err := s.db.Exec(ctx, `UPDATE loomplane_nodes SET status=$2,updated_at=now() WHERE id=$1 AND (revoked_at IS NULL OR $2='revoked')`, id, string(st))
	return affected(res, err)
}

// ClaimSession takes the node's session for a replica. The epoch is a counter
// on the node row, so it keeps rising across releases and a stale connection
// can never match a newer row. A missing or revoked node gives ErrNotFound.
func (s *PgStore) ClaimSession(ctx context.Context, sess Session) (int64, error) {
	var epoch int64
	err := s.db.QueryRow(ctx, `WITH e AS (UPDATE loomplane_nodes SET session_epoch=session_epoch+1 WHERE id=$1 AND revoked_at IS NULL RETURNING session_epoch)
INSERT INTO loomplane_node_sessions(node_id,epoch,replica,remote_addr,connected_at) SELECT $1,session_epoch,$2,$3,$4 FROM e
ON CONFLICT (node_id) DO UPDATE SET epoch=EXCLUDED.epoch,replica=EXCLUDED.replica,remote_addr=EXCLUDED.remote_addr,connected_at=EXCLUDED.connected_at
RETURNING epoch`, sess.NodeID, sess.Replica, sess.RemoteAddr, sess.ConnectedAt).Scan(&epoch)
	if err != nil {
		return 0, storeError(err)
	}
	return epoch, nil
}

// Session returns the node's live session, or ErrNotFound.
func (s *PgStore) Session(ctx context.Context, nodeID string) (*Session, error) {
	var out Session
	err := s.db.QueryRow(ctx, `SELECT node_id,epoch,replica,remote_addr,connected_at FROM loomplane_node_sessions WHERE node_id=$1`, nodeID).
		Scan(&out.NodeID, &out.Epoch, &out.Replica, &out.RemoteAddr, &out.ConnectedAt)
	if err != nil {
		return nil, storeError(err)
	}
	out.ConnectedAt = out.ConnectedAt.UTC()
	return &out, nil
}

// ReleaseSession deletes the session only when its epoch is the caller's, so
// a fenced connection can't drop its successor's row.
func (s *PgStore) ReleaseSession(ctx context.Context, nodeID string, epoch int64) error {
	_, err := s.db.Exec(ctx, `DELETE FROM loomplane_node_sessions WHERE node_id=$1 AND epoch=$2`, nodeID, epoch)
	return err
}

// MarkStaleOffline sets online nodes last seen before the cutoff to offline
// and returns their ids.
func (s *PgStore) MarkStaleOffline(ctx context.Context, before time.Time) ([]string, error) {
	rows, err := s.db.Query(ctx, `UPDATE loomplane_nodes SET status='offline',updated_at=now() WHERE status='online' AND COALESCE(last_seen_at,updated_at) < $1 RETURNING id`, before)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// ApplyInventory writes the inventory, version and protocol version a node
// reported, on a node that is not revoked. Endpoints and status stay as they
// are. Inventory is not in the event payload; a version change is.
func (s *PgStore) ApplyInventory(ctx context.Context, id string, inv nodeproto.Inventory, version string, protocolVersion int) error {
	raw, err := json.Marshal(inv)
	if err != nil {
		return err
	}
	res, err := s.db.Exec(ctx, `UPDATE loomplane_nodes SET inventory=$2,version=$3,protocol_version=$4,updated_at=now() WHERE id=$1 AND revoked_at IS NULL`, id, raw, version, protocolVersion)
	return affected(res, err)
}

// AllowEndpoints unions add into the endpoints of a node that is not revoked,
// keeping the stored order and appending what is new.
func (s *PgStore) AllowEndpoints(ctx context.Context, id string, add []nodeproto.Endpoint) (*Node, error) {
	uniq := []nodeproto.Endpoint{}
	for _, e := range add {
		if !slices.Contains(uniq, e) {
			uniq = append(uniq, e)
		}
	}
	raw, err := json.Marshal(uniq)
	if err != nil {
		return nil, err
	}
	return scanNode(s.db.QueryRow(ctx, `UPDATE loomplane_nodes SET endpoints=endpoints||COALESCE((SELECT jsonb_agg(x.e ORDER BY x.i) FROM jsonb_array_elements($2::jsonb) WITH ORDINALITY AS x(e,i) WHERE NOT loomplane_nodes.endpoints @> jsonb_build_array(x.e)),'[]'::jsonb),updated_at=now() WHERE id=$1 AND revoked_at IS NULL RETURNING `+nodeColumns, id, string(raw)))
}

// UseNonce records a challenge nonce as spent. The primary key makes a second
// use, on any replica, fail with ErrNonceUsed.
func (s *PgStore) UseNonce(ctx context.Context, nonce string, expires time.Time) error {
	res, err := s.db.Exec(ctx, `INSERT INTO loomplane_node_nonces(nonce,expires_at) VALUES($1,$2) ON CONFLICT (nonce) DO NOTHING`, nonce, expires)
	if err = affected(res, err); errors.Is(err, ErrNotFound) {
		return ErrNonceUsed
	}
	return err
}

// DeleteExpiredNonces drops spent nonces whose expiry is before now. They
// can't be replayed anyway, since an expired nonce is refused before use.
func (s *PgStore) DeleteExpiredNonces(ctx context.Context, now time.Time) error {
	_, err := s.db.Exec(ctx, `DELETE FROM loomplane_node_nonces WHERE expires_at < $1`, now)
	return err
}
