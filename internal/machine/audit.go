package machine

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/hermeshai/hermesh/pkg/identity"
)

// Audit is an append-only, hash-chained JSONL log. Every 100 records the
// chain head is signed with the machine key.
type Audit struct {
	dir    string
	signer identity.Signer

	mu   sync.Mutex
	seq  uint64
	prev string
	// f is the open file for month (records are appended to one file per month).
	f     *os.File
	month string
}

type AuditRecord struct {
	Seq      uint64 `json:"seq"`
	TS       string `json:"ts"`
	Prev     string `json:"prev"`
	Kind     string `json:"kind"`
	Subject  string `json:"subject,omitempty"`
	Session  string `json:"session,omitempty"`
	Action   string `json:"action,omitempty"`
	Decision string `json:"decision,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Detail   string `json:"detail,omitempty"`
	Sig      string `json:"sig,omitempty"` // checkpoint signature over Prev
	Hash     string `json:"hash"`
}

func recordHash(r AuditRecord) string {
	r.Hash = ""
	b, _ := json.Marshal(r)
	h := sha256.Sum256(append([]byte(r.Prev), b...))
	return hex.EncodeToString(h[:])
}

func OpenAudit(dir string, signer identity.Signer) (*Audit, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	a := &Audit{dir: dir, signer: signer}
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(files)
	if len(files) > 0 {
		last, err := lastRecord(files[len(files)-1])
		if err != nil {
			return nil, err
		}
		if last != nil {
			a.seq, a.prev = last.Seq, last.Hash
		}
	}
	return a, nil
}

func lastRecord(path string) (*AuditRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var last *AuditRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 4<<20)
	for sc.Scan() {
		r := &AuditRecord{}
		if json.Unmarshal(sc.Bytes(), r) == nil {
			last = r
		}
	}
	return last, sc.Err()
}

func (a *Audit) Log(r AuditRecord) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.appendLocked(r); err != nil {
		fmt.Fprintln(os.Stderr, "audit:", err)
		return
	}
	if a.seq%100 == 0 && a.signer != nil {
		sig, err := a.signer.SignMessage([]byte("hermesh-audit-v1\x00" + a.prev))
		if err == nil {
			a.appendLocked(AuditRecord{Kind: "checkpoint", Sig: base64.StdEncoding.EncodeToString(sig)})
		}
	}
}

func (a *Audit) appendLocked(r AuditRecord) error {
	now := time.Now().UTC()
	a.seq++
	r.Seq, r.TS, r.Prev = a.seq, now.Format(time.RFC3339Nano), a.prev
	r.Hash = recordHash(r)
	b, _ := json.Marshal(r)
	if month := now.Format("2006-01"); a.f == nil || month != a.month {
		f, err := os.OpenFile(filepath.Join(a.dir, month+".jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		if a.f != nil {
			a.f.Close()
		}
		a.f, a.month = f, month
	}
	if _, err := a.f.Write(append(b, '\n')); err != nil {
		return err
	}
	a.prev = r.Hash
	return nil
}

// VerifyAudit checks the whole chain and every checkpoint signature, made
// by one of keys (the machine's, and those it had before; none: chain only).
func VerifyAudit(dir string, keys ...identity.Signer) (records int, err error) {
	files, _ := filepath.Glob(filepath.Join(dir, "*.jsonl"))
	sort.Strings(files)
	prev := ""
	for _, path := range files {
		f, err := os.Open(path)
		if err != nil {
			return records, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 4<<20)
		for sc.Scan() {
			var r AuditRecord
			if err := json.Unmarshal(sc.Bytes(), &r); err != nil {
				f.Close()
				return records, fmt.Errorf("%s: bad record: %w", path, err)
			}
			if r.Prev != prev || recordHash(r) != r.Hash {
				f.Close()
				return records, fmt.Errorf("%s: chain broken at seq %d", path, r.Seq)
			}
			if r.Kind == "checkpoint" && len(keys) > 0 {
				sig, _ := base64.StdEncoding.DecodeString(r.Sig)
				if !slices.ContainsFunc(keys, func(k identity.Signer) bool {
					return identity.VerifyRaw(k.Public(), []byte("hermesh-audit-v1\x00"+r.Prev), sig) == nil
				}) {
					f.Close()
					return records, errors.New("bad checkpoint signature at seq " + fmt.Sprint(r.Seq))
				}
			}
			prev = r.Hash
			records++
		}
		f.Close()
	}
	return records, nil
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:8])
}
