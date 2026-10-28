package relay

import (
	"database/sql"
	"errors"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	identityv1 "github.com/hermeshai/hermesh/gen/go/hermesh/identity/v1"
	relayv1 "github.com/hermeshai/hermesh/gen/go/hermesh/relay/v1"
	"github.com/hermeshai/hermesh/internal/sqlitedb"
	"github.com/hermeshai/hermesh/pkg/identity"
)

var migrations = []string{`
CREATE TABLE meta (key TEXT PRIMARY KEY, value BLOB NOT NULL);
CREATE TABLE machine_certs (
  machine_id TEXT PRIMARY KEY,
  cert BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE grants (
  grant_id TEXT PRIMARY KEY,
  client_id TEXT NOT NULL,
  version INTEGER NOT NULL,
  grant BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX grants_client ON grants(client_id);
CREATE TABLE pairing (
  code TEXT PRIMARY KEY,
  kind INTEGER NOT NULL,
  request BLOB NOT NULL,
  expires_at INTEGER NOT NULL,
  result BLOB
);
CREATE TABLE connection_log (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  ts INTEGER NOT NULL,
  role INTEGER NOT NULL,
  subject_id TEXT NOT NULL,
  remote TEXT NOT NULL,
  event TEXT NOT NULL
);
`, `
ALTER TABLE pairing ADD COLUMN owner_id TEXT NOT NULL DEFAULT '';
ALTER TABLE pairing ADD COLUMN account_hint TEXT NOT NULL DEFAULT '';
`, `
CREATE TABLE push_targets (
  device_key_id TEXT PRIMARY KEY,
  target BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);
`, `
CREATE TABLE messages (
  id TEXT PRIMARY KEY,
  inbox TEXT NOT NULL,
  sealed BLOB,
  state TEXT NOT NULL,
  state_at INTEGER NOT NULL,
  received_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX messages_waiting ON messages(inbox, received_at) WHERE sealed IS NOT NULL;
CREATE TABLE inbox_registrations (
  inbox TEXT PRIMARY KEY,
  registration BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE TABLE contact_cards (
  client_id TEXT PRIMARY KEY,
  card BLOB NOT NULL,
  device_cert BLOB NOT NULL,
  updated_at INTEGER NOT NULL
);
`, `
ALTER TABLE messages ADD COLUMN ring TEXT;
`, `
CREATE TABLE erase_orders (
  owner_id TEXT PRIMARY KEY,
  erase_id TEXT NOT NULL,
  erase_order BLOB NOT NULL,
  root_chain BLOB NOT NULL,
  erased_at INTEGER NOT NULL
);
CREATE TABLE erase_acks (
  erase_id TEXT NOT NULL,
  key_id TEXT NOT NULL,
  acked_at INTEGER NOT NULL,
  PRIMARY KEY (erase_id, key_id)
);
`}

// Store is the Backend of a self-hosted relay: one owner, SQLite. It keeps
// only public, signed objects and metadata, never a secret that could make
// a machine execute anything. The owner id arguments are ignored except that
// RootChain answers only for the claimed owner.
type Store struct{ db *sql.DB }

var (
	_ Backend      = (*Store)(nil)
	_ PushStore    = (*Store)(nil)
	_ MessageStore = (*Store)(nil)
)

func OpenStore(path string) (*Store, error) {
	db, err := sqlitedb.Open(path, migrations)
	if err != nil {
		return nil, err
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) getMeta(key string) ([]byte, error) {
	var v []byte
	err := s.db.QueryRow(`SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return v, err
}

func (s *Store) setMeta(key string, v []byte) error {
	_, err := s.db.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, v)
	return err
}

// RelayID returns the relay's random id, creating it on first use.
func (s *Store) RelayID() (string, error) {
	v, err := s.getMeta("relay_id")
	if err != nil || v != nil {
		return string(v), err
	}
	id := "r_" + strings.ToLower(identity.RandomCode(16))
	return id, s.setMeta("relay_id", []byte(id))
}

// ClaimCode returns the pending claim code, creating one if unclaimed.
func (s *Store) ClaimCode() (string, error) {
	v, err := s.getMeta("claim_code")
	if err != nil || v != nil {
		return string(v), err
	}
	c := identity.RandomCode(16)
	code := c[:4] + "-" + c[4:8] + "-" + c[8:12] + "-" + c[12:16]
	return code, s.setMeta("claim_code", []byte(code))
}

func (s *Store) OwnerID() (string, error) {
	v, err := s.getMeta("owner_id")
	return string(v), err
}

func (s *Store) SaveClaim(ownerID string, rootChain *identityv1.RootChain, deviceCert *identityv1.Signed) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rc, _ := proto.Marshal(rootChain)
	dc, _ := proto.Marshal(deviceCert)
	for k, v := range map[string][]byte{"owner_id": []byte(ownerID), "root_chain": rc, "device_cert": dc} {
		if _, err := tx.Exec(`INSERT INTO meta(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM meta WHERE key='claim_code'`); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RootChain(ownerID string) (*identityv1.RootChain, error) {
	if owner, err := s.OwnerID(); err != nil || owner == "" || owner != ownerID {
		return nil, err
	}
	v, err := s.getMeta("root_chain")
	if err != nil || v == nil {
		return nil, err
	}
	out := &identityv1.RootChain{}
	return out, proto.Unmarshal(v, out)
}

func (s *Store) SetRootChain(_ string, c *identityv1.RootChain) error {
	b, _ := proto.Marshal(c)
	return s.setMeta("root_chain", b)
}

func (s *Store) Revocations(string) (*identityv1.RevocationList, error) {
	v, err := s.getMeta("revocations")
	out := &identityv1.RevocationList{MinGrantVersions: map[string]uint64{}}
	if err != nil || v == nil {
		return out, err
	}
	if err := proto.Unmarshal(v, out); err != nil {
		return nil, err
	}
	if out.MinGrantVersions == nil {
		out.MinGrantVersions = map[string]uint64{}
	}
	return out, nil
}

func (s *Store) SetRevocations(_ string, r *identityv1.RevocationList) error {
	b, _ := proto.Marshal(r)
	return s.setMeta("revocations", b)
}

func (s *Store) PutMachineCert(_, machineID string, cert *identityv1.Signed) error {
	b, _ := proto.Marshal(cert)
	_, err := s.db.Exec(`INSERT INTO machine_certs(machine_id,cert,updated_at) VALUES(?,?,?)
		ON CONFLICT(machine_id) DO UPDATE SET cert=excluded.cert, updated_at=excluded.updated_at`, machineID, b, time.Now().Unix())
	return err
}

func (s *Store) DeleteMachineCert(_, machineID string) error {
	_, err := s.db.Exec(`DELETE FROM machine_certs WHERE machine_id=?`, machineID)
	return err
}

func (s *Store) MachineCerts(string) (map[string]*identityv1.Signed, error) {
	rows, err := s.db.Query(`SELECT machine_id, cert FROM machine_certs`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*identityv1.Signed{}
	for rows.Next() {
		var id string
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		c := &identityv1.Signed{}
		if err := proto.Unmarshal(b, c); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// PutGrant stores g if its version is newer than the stored one.
func (s *Store) PutGrant(_ string, g *identityv1.Grant, signed *identityv1.Signed) error {
	b, _ := proto.Marshal(signed)
	_, err := s.db.Exec(`INSERT INTO grants(grant_id,client_id,version,grant,updated_at) VALUES(?,?,?,?,?)
		ON CONFLICT(grant_id) DO UPDATE SET client_id=excluded.client_id, version=excluded.version, grant=excluded.grant, updated_at=excluded.updated_at
		WHERE excluded.version > grants.version`, g.GrantId, g.ClientId, g.Version, b, time.Now().Unix())
	return err
}

func (s *Store) DeleteGrant(_, grantID string) error {
	_, err := s.db.Exec(`DELETE FROM grants WHERE grant_id=?`, grantID)
	return err
}

func (s *Store) Grants(string) ([]*identityv1.Signed, error) {
	rows, err := s.db.Query(`SELECT grant FROM grants ORDER BY updated_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*identityv1.Signed
	for rows.Next() {
		var b []byte
		if err := rows.Scan(&b); err != nil {
			return nil, err
		}
		g := &identityv1.Signed{}
		if err := proto.Unmarshal(b, g); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func (s *Store) GrantForClient(_, clientID string) (*identityv1.Signed, error) {
	var b []byte
	err := s.db.QueryRow(`SELECT grant FROM grants WHERE client_id=? ORDER BY version DESC, updated_at DESC LIMIT 1`, clientID).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g := &identityv1.Signed{}
	return g, proto.Unmarshal(b, g)
}

func (s *Store) PutPairing(p *Pairing) error {
	b, _ := proto.Marshal(p.Request)
	_, err := s.db.Exec(`INSERT INTO pairing(code,kind,request,expires_at,owner_id) VALUES(?,?,?,?,?)
		ON CONFLICT(code) DO UPDATE SET request=excluded.request, expires_at=excluded.expires_at WHERE pairing.result IS NULL`,
		p.Code, int(p.Kind), b, p.ExpiresAt.Unix(), p.OwnerID)
	return err
}

func (s *Store) GetPairing(code string) (*Pairing, error) {
	var kind int
	var req, res []byte
	var exp int64
	p := &Pairing{Code: code, Request: &identityv1.Signed{}}
	err := s.db.QueryRow(`SELECT kind, request, expires_at, result, owner_id, account_hint FROM pairing WHERE code=?`, code).
		Scan(&kind, &req, &exp, &res, &p.OwnerID, &p.AccountHint)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Kind, p.ExpiresAt = identityv1.PairingKind(kind), time.Unix(exp, 0)
	if err := proto.Unmarshal(req, p.Request); err != nil {
		return nil, err
	}
	if res != nil {
		p.Result = &identityv1.Signed{}
		if err := proto.Unmarshal(res, p.Result); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (s *Store) ClaimPairing(code, ownerID, accountHint string) error {
	_, err := s.db.Exec(`UPDATE pairing SET owner_id=?, account_hint=? WHERE code=? AND owner_id='' AND result IS NULL`, ownerID, accountHint, code)
	return err
}

func (s *Store) CompletePairing(code string, result *identityv1.Signed) error {
	b, _ := proto.Marshal(result)
	_, err := s.db.Exec(`UPDATE pairing SET result=?, expires_at=? WHERE code=?`, b, time.Now().Add(time.Hour).Unix(), code)
	return err
}

func (s *Store) PendingPairings(now time.Time) (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM pairing WHERE result IS NULL AND expires_at > ?`, now.Unix()).Scan(&n)
	return n, err
}

func (s *Store) OwnerPendingPairings(ownerID string, now time.Time) ([]string, error) {
	rows, err := s.db.Query(`SELECT code FROM pairing WHERE result IS NULL AND expires_at > ? AND (owner_id=? OR owner_id='') ORDER BY expires_at DESC LIMIT 100`, now.Unix(), ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var codes []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		codes = append(codes, c)
	}
	return codes, rows.Err()
}

func (s *Store) PurgePairings(now time.Time) error {
	_, err := s.db.Exec(`DELETE FROM pairing WHERE expires_at <= ?`, now.Unix())
	return err
}

func (s *Store) LogConnection(_ string, role int, subjectID, remote, event string) {
	s.db.Exec(`INSERT INTO connection_log(ts,role,subject_id,remote,event) VALUES(?,?,?,?,?)`, time.Now().Unix(), role, subjectID, remote, event)
	s.db.Exec(`DELETE FROM connection_log WHERE ts < ?`, time.Now().Add(-30*24*time.Hour).Unix())
}

// PushTargets are the owner's push registrations, by device key id.
func (s *Store) PushTargets(string) (map[string]*relayv1.PushTarget, error) {
	rows, err := s.db.Query(`SELECT device_key_id, target FROM push_targets`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*relayv1.PushTarget{}
	for rows.Next() {
		var id string
		var b []byte
		if err := rows.Scan(&id, &b); err != nil {
			return nil, err
		}
		t := &relayv1.PushTarget{}
		if err := proto.Unmarshal(b, t); err != nil {
			return nil, err
		}
		out[id] = t
	}
	return out, rows.Err()
}

func (s *Store) PutPushTarget(_, deviceKeyID string, t *relayv1.PushTarget) error {
	b, err := proto.Marshal(t)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO push_targets (device_key_id, target, updated_at) VALUES (?, ?, ?)
ON CONFLICT(device_key_id) DO UPDATE SET target = excluded.target, updated_at = excluded.updated_at`, deviceKeyID, b, time.Now().Unix())
	return err
}

func (s *Store) DeletePushTarget(_, deviceKeyID string) error {
	_, err := s.db.Exec(`DELETE FROM push_targets WHERE device_key_id = ?`, deviceKeyID)
	return err
}

// The post office (messages.go). Times are Unix nanoseconds, so messages
// keep their order of arrival.

func (s *Store) PutMessage(_ string, m Message) error {
	_, err := s.db.Exec(`INSERT INTO messages (id, inbox, sealed, state, state_at, received_at, expires_at, ring) VALUES (?, ?, ?, 'waiting', ?, ?, ?, ?)
ON CONFLICT(id) DO NOTHING`, m.ID, m.Inbox, m.Sealed, m.ReceivedAt.UnixNano(), m.ReceivedAt.UnixNano(), m.ExpiresAt.UnixNano(), m.Ring)
	return err
}

func (s *Store) WaitingMessages(_, inbox string) ([]Message, error) {
	rows, err := s.db.Query(`SELECT id, sealed, received_at, expires_at, ring FROM messages WHERE inbox = ? AND sealed IS NOT NULL ORDER BY received_at, id`, inbox)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Message
	for rows.Next() {
		m := Message{Inbox: inbox}
		var received, expires int64
		if err := rows.Scan(&m.ID, &m.Sealed, &received, &expires, &m.Ring); err != nil {
			return nil, err
		}
		m.ReceivedAt, m.ExpiresAt = time.Unix(0, received), time.Unix(0, expires)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *Store) SetMessageStates(_, inbox string, ids []string, state string, at time.Time) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	q := `UPDATE messages SET state = ?, state_at = ?, sealed = NULL WHERE id = ? AND inbox = ? AND sealed IS NOT NULL`
	if state == "taken" {
		q = `UPDATE messages SET state = ?, state_at = ? WHERE id = ? AND inbox = ? AND sealed IS NOT NULL`
	}
	for _, id := range ids {
		if _, err := tx.Exec(q, state, at.UnixNano(), id, inbox); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) MessageStates(_ string, ids []string) (map[string]MessageState, error) {
	out := map[string]MessageState{}
	for _, id := range ids {
		var st string
		var at int64
		err := s.db.QueryRow(`SELECT state, state_at FROM messages WHERE id = ?`, id).Scan(&st, &at)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out[id] = MessageState{State: st, At: time.Unix(0, at)}
	}
	return out, nil
}

func (s *Store) ExpireMessages(now time.Time) error {
	if _, err := s.db.Exec(`UPDATE messages SET state = 'expired', state_at = ?, sealed = NULL WHERE sealed IS NOT NULL AND expires_at < ?`, now.UnixNano(), now.UnixNano()); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM messages WHERE expires_at < ?`, now.Add(-MessageRetention).UnixNano())
	return err
}

func (s *Store) DropInbox(_, inbox string) error {
	now := time.Now().UnixNano()
	if _, err := s.db.Exec(`UPDATE messages SET state = 'expired', state_at = ?, sealed = NULL WHERE inbox = ? AND sealed IS NOT NULL`, now, inbox); err != nil {
		return err
	}
	if _, err := s.db.Exec(`DELETE FROM inbox_registrations WHERE inbox = ?`, inbox); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM contact_cards WHERE client_id = ?`, inbox)
	return err
}

func (s *Store) PutRegistration(_, inbox string, reg *identityv1.Signed) error {
	b, err := proto.Marshal(reg)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO inbox_registrations (inbox, registration, updated_at) VALUES (?, ?, ?)
ON CONFLICT(inbox) DO UPDATE SET registration = excluded.registration, updated_at = excluded.updated_at`, inbox, b, time.Now().Unix())
	return err
}

func (s *Store) Registrations(string) (map[string]*identityv1.Signed, error) {
	rows, err := s.db.Query(`SELECT inbox, registration FROM inbox_registrations`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]*identityv1.Signed{}
	for rows.Next() {
		var inbox string
		var b []byte
		if err := rows.Scan(&inbox, &b); err != nil {
			return nil, err
		}
		reg := &identityv1.Signed{}
		if err := proto.Unmarshal(b, reg); err != nil {
			return nil, err
		}
		out[inbox] = reg
	}
	return out, rows.Err()
}

func (s *Store) PutCard(_, clientID string, card, deviceCert *identityv1.Signed) error {
	cb, err := proto.Marshal(card)
	if err != nil {
		return err
	}
	db, err := proto.Marshal(deviceCert)
	if err != nil {
		return err
	}
	_, err = s.db.Exec(`INSERT INTO contact_cards (client_id, card, device_cert, updated_at) VALUES (?, ?, ?, ?)
ON CONFLICT(client_id) DO UPDATE SET card = excluded.card, device_cert = excluded.device_cert, updated_at = excluded.updated_at`, clientID, cb, db, time.Now().Unix())
	return err
}

func (s *Store) Cards(string) (map[string]StoredCard, error) {
	rows, err := s.db.Query(`SELECT client_id, card, device_cert FROM contact_cards`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]StoredCard{}
	for rows.Next() {
		var id string
		var cb, db []byte
		if err := rows.Scan(&id, &cb, &db); err != nil {
			return nil, err
		}
		c := StoredCard{Card: &identityv1.Signed{}, DeviceCert: &identityv1.Signed{}}
		if err := proto.Unmarshal(cb, c.Card); err != nil {
			return nil, err
		}
		if err := proto.Unmarshal(db, c.DeviceCert); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// EraseOwner keeps the order and leaves the relay unclaimed, as it was
// before its first owner: a new claim code is made when one is asked for.
func (s *Store) EraseOwner(ownerID, eraseID string, order *identityv1.Signed, chain *identityv1.RootChain, at time.Time) error {
	ob, _ := proto.Marshal(order)
	cb, _ := proto.Marshal(chain)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO erase_orders(owner_id, erase_id, erase_order, root_chain, erased_at) VALUES(?,?,?,?,?)
ON CONFLICT(owner_id) DO UPDATE SET erase_id=excluded.erase_id, erase_order=excluded.erase_order, root_chain=excluded.root_chain, erased_at=excluded.erased_at`,
		ownerID, eraseID, ob, cb, at.Unix()); err != nil {
		return err
	}
	for _, q := range []string{
		`DELETE FROM meta WHERE key IN ('owner_id', 'root_chain', 'device_cert', 'revocations', 'claim_code')`,
		`DELETE FROM machine_certs`, `DELETE FROM grants`, `DELETE FROM pairing`, `DELETE FROM push_targets`,
		`DELETE FROM messages`, `DELETE FROM inbox_registrations`, `DELETE FROM contact_cards`, `DELETE FROM connection_log`,
	} {
		if _, err := tx.Exec(q); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) EraseOrder(ownerID string, now time.Time) (*identityv1.Signed, *identityv1.RootChain, []string, error) {
	var eraseID string
	var ob, cb []byte
	var at int64
	err := s.db.QueryRow(`SELECT erase_id, erase_order, root_chain, erased_at FROM erase_orders WHERE owner_id=?`, ownerID).Scan(&eraseID, &ob, &cb, &at)
	if errors.Is(err, sql.ErrNoRows) || err == nil && now.Sub(time.Unix(at, 0)) > EraseRetention {
		return nil, nil, nil, nil
	}
	if err != nil {
		return nil, nil, nil, err
	}
	order, chain := &identityv1.Signed{}, &identityv1.RootChain{}
	if err := proto.Unmarshal(ob, order); err != nil {
		return nil, nil, nil, err
	}
	if err := proto.Unmarshal(cb, chain); err != nil {
		return nil, nil, nil, err
	}
	rows, err := s.db.Query(`SELECT key_id FROM erase_acks WHERE erase_id=? ORDER BY acked_at`, eraseID)
	if err != nil {
		return nil, nil, nil, err
	}
	defer rows.Close()
	var acks []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, nil, nil, err
		}
		acks = append(acks, k)
	}
	return order, chain, acks, rows.Err()
}

// maxEraseAcks bounds what anyone who knows an erase id can make a relay
// keep: an owner has far fewer devices, machines and agents.
const maxEraseAcks = 1000

func (s *Store) AckErase(eraseID, keyID string, at time.Time) error {
	_, err := s.db.Exec(`INSERT INTO erase_acks(erase_id, key_id, acked_at) SELECT ?, ?, ?
WHERE EXISTS (SELECT 1 FROM erase_orders WHERE erase_id=?) AND (SELECT COUNT(*) FROM erase_acks WHERE erase_id=?) < ?
ON CONFLICT(erase_id, key_id) DO NOTHING`, eraseID, keyID, at.Unix(), eraseID, eraseID, maxEraseAcks)
	return err
}
