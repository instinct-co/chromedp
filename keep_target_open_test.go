package chromedp

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/target"
)

func targetOpen(t *testing.T, ctx context.Context, id target.ID) bool {
	t.Helper()
	infos, err := Targets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range infos {
		if info.TargetID == id {
			return true
		}
	}
	return false
}

// A context marked with KeepTargetOpen leaves its page open when it is
// cancelled; an unmarked one still closes its page, as before.
func TestKeepTargetOpen(t *testing.T) {
	t.Parallel()
	for _, keep := range []bool{true, false} {
		ctx, cancel := NewContext(browserCtxForTest(t))
		if err := Run(ctx); err != nil {
			t.Fatal(err)
		}
		id := FromContext(ctx).Target.TargetID
		if keep {
			KeepTargetOpen(ctx)
		}
		cancel()
		// A closed page can linger in the target list for a moment after
		// Target.closeTarget returns; a kept page must stay throughout.
		open := targetOpen(t, browserCtxForTest(t), id)
		for deadline := time.Now().Add(5 * time.Second); open && !keep && time.Now().Before(deadline); open = targetOpen(t, browserCtxForTest(t), id) {
			time.Sleep(20 * time.Millisecond)
		}
		if open != keep {
			t.Fatalf("keep=%v: page open after cancel = %v", keep, open)
		}
		if keep {
			// Clean up the page the test kept.
			if err := target.CloseTarget(id).Do(cdpBrowserExecutor(t)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

// KeepTargetOpen may run on one goroutine while another cancels the same
// context; under -race this must not report a data race, whichever wins.
func TestKeepTargetOpenConcurrentWithCancel(t *testing.T) {
	t.Parallel()
	for range 50 {
		ctx, cancel := NewContext(browserCtxForTest(t))
		if err := Run(ctx); err != nil {
			t.Fatal(err)
		}
		id := FromContext(ctx).Target.TargetID
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); KeepTargetOpen(ctx) }()
		go func() { defer wg.Done(); cancel() }()
		wg.Wait()
		if targetOpen(t, browserCtxForTest(t), id) {
			_ = target.CloseTarget(id).Do(cdpBrowserExecutor(t))
		}
	}
}

func TestKeepTargetOpenIgnoresOtherContexts(t *testing.T) {
	t.Parallel()
	KeepTargetOpen(context.Background()) // must not panic
}

func browserCtxForTest(t *testing.T) context.Context {
	t.Helper()
	allocateOnce.Do(func() { browserCtx, _ = testAllocateSeparate(t) })
	if browserCtx == nil {
		t.FailNow()
	}
	return browserCtx
}

func cdpBrowserExecutor(t *testing.T) context.Context {
	t.Helper()
	c := FromContext(browserCtxForTest(t))
	return cdp.WithExecutor(context.Background(), c.Browser)
}
