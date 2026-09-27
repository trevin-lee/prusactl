package auth

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

const doneHTML = `<!doctype html><meta charset="utf-8"><title>Signed in</title>
<body style="font:16px -apple-system,system-ui,sans-serif;display:grid;place-items:center;height:90vh;color:#222">
<div style="text-align:center"><h2>Signed in to Prusa Connect</h2>
<p>prusactl has your session. This window will close by itself.</p></div>`

// BrowserLogin opens a dedicated Chrome window on Prusa's own sign-in page and
// waits for the user to finish. Prusa Account then redirects the window to
// the Connect callback URL; that navigation is intercepted before it leaves
// the machine, so the authorization code is redeemed here (with our PKCE
// verifier) instead of by the Connect web app.
//
// The window uses a throwaway profile that is deleted afterwards: nothing
// from the user's everyday browser is read, and no cookies persist.
func (c Config) BrowserLogin(ctx context.Context, timeout time.Duration) (*Token, error) {
	p, err := newPKCE()
	if err != nil {
		return nil, err
	}

	opts := []chromedp.ExecAllocatorOption{
		chromedp.NoFirstRun,
		chromedp.NoDefaultBrowserCheck,
		chromedp.WindowSize(560, 820),
		// Keep the window looking like ordinary Chrome so identity providers
		// (Google/Apple sign-in on Prusa Account) don't refuse it.
		chromedp.Flag("disable-blink-features", "AutomationControlled"),
		chromedp.Flag("use-mock-keychain", true),
		chromedp.Flag("password-store", "basic"),
		chromedp.Flag("disable-sync", true),
	}
	if path := browserPath(); path != "" {
		opts = append(opts, chromedp.ExecPath(path))
	}
	allocCtx, cancelAlloc := chromedp.NewExecAllocator(ctx, opts...)
	defer cancelAlloc()
	browserCtx, cancelBrowser := chromedp.NewContext(allocCtx)
	defer cancelBrowser()

	waitCtx, cancelWait := context.WithTimeout(browserCtx, timeout)
	defer cancelWait()

	callbacks := make(chan string, 1)
	chromedp.ListenTarget(browserCtx, func(ev any) {
		paused, ok := ev.(*fetch.EventRequestPaused)
		if !ok {
			return
		}
		// Handlers must not block the event loop; act from a goroutine.
		go func() {
			exec := cdp.WithExecutor(browserCtx, chromedp.FromContext(browserCtx).Target)
			_ = fetch.FulfillRequest(paused.RequestID, 200).
				WithResponseHeaders([]*fetch.HeaderEntry{{Name: "Content-Type", Value: "text/html; charset=utf-8"}}).
				WithBody(base64.StdEncoding.EncodeToString([]byte(doneHTML))).
				Do(exec)
			select {
			case callbacks <- paused.Request.URL:
			default:
			}
		}()
	})

	err = chromedp.Run(browserCtx,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{{
			URLPattern:   c.RedirectURI + "*",
			RequestStage: fetch.RequestStageRequest,
		}}),
		chromedp.Navigate(c.authorizeURL(p)),
	)
	if err != nil {
		return nil, fmt.Errorf("opening Chrome (is Google Chrome installed?): %w", err)
	}

	closed := watchForClose(browserCtx)
	var callback string
	select {
	case callback = <-callbacks:
	case <-closed:
		return nil, errors.New("the sign-in window was closed before sign-in finished")
	case <-waitCtx.Done():
		switch {
		case ctx.Err() != nil:
			return nil, ctx.Err()
		case errors.Is(waitCtx.Err(), context.DeadlineExceeded):
			return nil, fmt.Errorf("gave up waiting for sign-in after %s", timeout)
		default: // chromedp cancels the tab's context when the tab goes away
			return nil, errors.New("the sign-in window was closed before sign-in finished")
		}
	}

	code, err := c.codeFromCallback(callback, p)
	if err != nil {
		return nil, err
	}
	tok, err := c.exchangeCode(ctx, code, p)
	if err != nil {
		return nil, err
	}
	// Let the user see the confirmation page for a moment.
	select {
	case <-time.After(1200 * time.Millisecond):
	case <-closed:
	}
	return tok, nil
}

// browserPath picks the browser for the sign-in window: PRUSACTL_BROWSER if
// set, else Google Chrome ahead of other Chromium builds on macOS (chromedp's
// own search prefers Chromium). Empty means let chromedp search.
func browserPath() string {
	if p := os.Getenv("PRUSACTL_BROWSER"); p != "" {
		return p
	}
	if runtime.GOOS != "darwin" {
		return ""
	}
	for _, p := range []string{
		"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
		"/Applications/Chromium.app/Contents/MacOS/Chromium",
		"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
	} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return ""
}

// watchForClose reports when the user has closed every page in the window
// (on macOS, closing the window leaves Chrome running with no pages). The
// channel is closed only on a detected close, never because ctx ended.
func watchForClose(ctx context.Context) <-chan struct{} {
	done := make(chan struct{})
	go func() {
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
			}
			var infos []*target.Info
			err := chromedp.Run(ctx, chromedp.ActionFunc(func(ctx context.Context) error {
				var err error
				infos, err = target.GetTargets().Do(cdp.WithExecutor(ctx, chromedp.FromContext(ctx).Browser))
				return err
			}))
			if ctx.Err() != nil {
				return
			}
			pages := 0
			for _, t := range infos {
				if t.Type == "page" {
					pages++
				}
			}
			if err != nil || pages == 0 {
				close(done)
				return
			}
		}
	}()
	return done
}
