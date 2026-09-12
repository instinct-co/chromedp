package chromedp

import (
	"context"
	"errors"
	"io"
	"net"
	"os/exec"
	"testing"
	"testing/synctest"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func TestBrowserDisconnectUnblocksTransport(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, peer := net.Pipe()
		defer client.Close()
		defer peer.Close()
		conn := &Conn{
			conn:   client,
			writer: *wsutil.NewWriterBufferSize(client, ws.StateClientSide, ws.OpText, 0),
		}
		b := &Browser{conn: conn}
		readDone, writeDone := make(chan error, 1), make(chan error, 1)
		go func() {
			readDone <- conn.Read(context.Background(), new(cdproto.Message))
		}()
		go func() {
			writeDone <- conn.Write(context.Background(), &cdproto.Message{ID: 1, Method: target.CommandGetTargets})
		}()
		synctest.Wait()
		select {
		case err := <-readDone:
			t.Fatalf("read returned before disconnect: %v", err)
		case err := <-writeDone:
			t.Fatalf("write returned before disconnect: %v", err)
		default:
		}
		if err := b.Disconnect(); err != nil {
			t.Fatal(err)
		}
		if err := <-readDone; err == nil {
			t.Fatal("blocked read succeeded after disconnect")
		}
		if err := <-writeDone; err == nil {
			t.Fatal("blocked write succeeded after disconnect")
		}
		var buf [1]byte
		if n, err := peer.Read(buf[:]); n != 0 || !errors.Is(err, io.EOF) {
			t.Fatalf("peer received data after disconnect: n=%d, err=%v", n, err)
		}
	})
}

type disconnectErrorTransport struct {
	Transport
	err error
}

func (c disconnectErrorTransport) Close() error { return c.err }

func TestBrowserDisconnectReturnsTransportError(t *testing.T) {
	want := errors.New("transport close failed")
	b := &Browser{conn: disconnectErrorTransport{err: want}}
	if err := b.Disconnect(); err != want {
		t.Fatalf("Disconnect error = %v, want %v", err, want)
	}
}

func TestBrowserDisconnectPreservesExternalChromeTarget(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, execPath,
		"--no-first-run",
		"--no-default-browser-check",
		"--headless",
		"--disable-gpu",
		"--no-sandbox",
		"--user-data-dir="+t.TempDir(),
		"--remote-debugging-port=0",
		"about:blank",
	)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stderr.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cancel()
		cmd.Wait()
	}()
	wsURL, _, err := readOutput(stderr, nil)
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewBrowser(ctx, wsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Disconnect()
	targetID, err := target.CreateTarget("about:blank").Do(cdp.WithExecutor(ctx, first))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Disconnect(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-first.LostConnection:
	case <-ctx.Done():
		t.Fatal("browser did not observe disconnection:", ctx.Err())
	}
	second, err := NewBrowser(ctx, wsURL)
	if err != nil {
		t.Fatalf("reconnect to external Chrome: %v", err)
	}
	defer second.Disconnect()
	info, err := target.GetTargetInfo().WithTargetID(targetID).Do(cdp.WithExecutor(ctx, second))
	if err != nil {
		t.Fatalf("target did not survive disconnect: %v", err)
	}
	if info.TargetID != targetID {
		t.Fatalf("target ID = %q, want %q", info.TargetID, targetID)
	}
}
