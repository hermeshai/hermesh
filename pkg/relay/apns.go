package relay

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// APNs sends notifications through Apple's push service with token-based
// authentication (a .p8 key from the developer account).
type APNs struct {
	// KeyID and TeamID identify the key; Topic is the app's bundle id.
	KeyID, TeamID, Topic string
	key                  *ecdsa.PrivateKey
	client               *http.Client

	mu     sync.Mutex
	token  string
	issued time.Time
}

// NewAPNs reads the .p8 key at keyFile.
func NewAPNs(keyFile, keyID, teamID, topic string) (*APNs, error) {
	b, err := os.ReadFile(keyFile)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil {
		return nil, errors.New("the APNs key is not PEM")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	ek, ok := k.(*ecdsa.PrivateKey)
	if !ok {
		return nil, errors.New("the APNs key is not an EC key")
	}
	if keyID == "" || teamID == "" || topic == "" {
		return nil, errors.New("APNs needs the key id, team id and topic")
	}
	return &APNs{KeyID: keyID, TeamID: teamID, Topic: topic, key: ek, client: &http.Client{Timeout: 30 * time.Second}}, nil
}

// bearer is the provider token: an ES256 JWT, renewed every 50 minutes
// (Apple takes them for an hour, and refuses renewing more than once in 20).
func (a *APNs) bearer() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token != "" && time.Since(a.issued) < 50*time.Minute {
		return a.token, nil
	}
	enc := base64.RawURLEncoding
	head, _ := json.Marshal(map[string]string{"alg": "ES256", "kid": a.KeyID})
	now := time.Now()
	claims, _ := json.Marshal(map[string]any{"iss": a.TeamID, "iat": now.Unix()})
	unsigned := enc.EncodeToString(head) + "." + enc.EncodeToString(claims)
	h := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, a.key, h[:])
	if err != nil {
		return "", err
	}
	sig := make([]byte, 64)
	r.FillBytes(sig[:32])
	s.FillBytes(sig[32:])
	a.token, a.issued = unsigned+"."+enc.EncodeToString(sig), now
	return a.token, nil
}

func (a *APNs) Push(ctx context.Context, p Push) error {
	host := "https://api.push.apple.com"
	if p.Environment == "development" {
		host = "https://api.sandbox.push.apple.com"
	}
	tok, err := a.bearer()
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/3/device/"+p.Token, bytes.NewReader(p.Payload))
	if err != nil {
		return err
	}
	req.Header.Set("authorization", "bearer "+tok)
	req.Header.Set("apns-topic", a.Topic)
	req.Header.Set("apns-push-type", "alert")
	req.Header.Set("apns-expiration", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
	if p.Urgent {
		req.Header.Set("apns-priority", "10")
	} else {
		req.Header.Set("apns-priority", "5")
	}
	res, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(res.Body, 512))
		return fmt.Errorf("apns: %s %s", res.Status, body)
	}
	return nil
}

// Spool writes each notification to a file in Dir as JSON ({"token",
// "environment", "payload"}), for tests: a Simulator gets them with `xcrun
// simctl push`.
type Spool struct{ Dir string }

var spoolSafe = regexp.MustCompile(`[^A-Za-z0-9_-]`)

func (s Spool) Push(_ context.Context, p Push) error {
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	b, _ := json.Marshal(map[string]any{"token": p.Token, "environment": p.Environment, "payload": json.RawMessage(p.Payload)})
	name := fmt.Sprintf("%d-%s.json", time.Now().UnixNano(), spoolSafe.ReplaceAllString(p.Thread, ""))
	tmp := filepath.Join(s.Dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.Dir, name))
}
