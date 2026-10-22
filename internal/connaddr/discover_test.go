package connaddr

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDiscover(t *testing.T) {
	body := ""
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != DiscoveryPath {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	ctx := context.Background()
	for _, c := range []struct {
		body   string
		status int
		want   string
		bad    bool
	}{
		{`{"connect_url":"https://connect.example.com"}`, 200, "https://connect.example.com", false},
		{`{"connect_url":"https://connect.example.com/"}`, 200, "https://connect.example.com", false},
		{`{"connect_url":"http://localhost:8080"}`, 200, "http://localhost:8080", false}, // http only next to an http center
		{`{}`, 200, srv.URL, false},
		{``, 404, srv.URL, false}, // a self-hosted relay publishes nothing
		{`{"connect_url":"https://connect.example.com/x?y"}`, 200, "", true},
		{`{"connect_url":"ftp://connect.example.com"}`, 200, "", true},
		{`not json`, 200, "", true},
		{``, 500, "", true},
	} {
		body, status = c.body, c.status
		got, err := Discover(ctx, srv.Client(), srv.URL)
		if (err != nil) != c.bad || got != c.want {
			t.Errorf("%d %s: %q, %v", c.status, c.body, got, err)
		}
	}
	// A plain http connect URL is refused when the center itself is https.
	tls := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"connect_url":"http://connect.example.com"}`))
	}))
	defer tls.Close()
	if got, err := Discover(ctx, tls.Client(), tls.URL); err == nil {
		t.Fatalf("downgraded to %q", got)
	}
}

func TestCenter(t *testing.T) {
	t.Setenv("HERMESH_CENTER", "")
	if got := Center(""); got != OfficialCenter {
		t.Fatalf("default %q", got)
	}
	t.Setenv("HERMESH_CENTER", "https://center.example.com")
	if got := Center(""); got != "https://center.example.com" {
		t.Fatalf("from the environment %q", got)
	}
	if got := Center("relay.example.com/o_abcdefghijklmnopqrstuvwx"); got != "relay.example.com/o_abcdefghijklmnopqrstuvwx" {
		t.Fatalf("argument %q", got)
	}
}
