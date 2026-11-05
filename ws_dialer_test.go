package chromedp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gobwas/ws"
)

// A browser behind wss:// with a private certificate is reachable only
// through a dialer that trusts it (WithWSDialer); the default dialer refuses.
func TestWithWSDialer(t *testing.T) {
	t.Parallel()
	accepted := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		accepted <- struct{}{}
		time.Sleep(100 * time.Millisecond)
		conn.Close()
	}))
	defer server.Close()
	url := "wss" + strings.TrimPrefix(server.URL, "https")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := dialContextWith(ctx, ws.DefaultDialer, url); err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Fatalf("default dialer: got %v, want a certificate error", err)
	}

	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	browser := &Browser{}
	WithWSDialer(ws.Dialer{TLSConfig: &tls.Config{RootCAs: roots, ServerName: "example.com"}})(browser)
	if browser.wsDialer == nil {
		t.Fatal("WithWSDialer did not set the dialer")
	}
	conn, err := dialContextWith(ctx, *browser.wsDialer, url)
	if err != nil {
		t.Fatalf("pinned dialer: %v", err)
	}
	defer conn.Close()
	select {
	case <-accepted:
	case <-ctx.Done():
		t.Fatal("server never accepted the websocket")
	}
}
