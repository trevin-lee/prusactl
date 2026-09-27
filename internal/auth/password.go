package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

// Prompter asks the user for sign-in details. prusactl implements it with
// terminal prompts; nothing it returns is stored.
type Prompter interface {
	Email() (string, error)
	Password() (string, error)
	OneTimeCode() (string, error) // 2FA code, only asked if the account uses it
}

// PasswordLogin signs in without a browser: it walks Prusa Account's own
// login pages over HTTP (the same form a browser would submit), then stops at
// the redirect back to Connect and redeems the code with our PKCE verifier.
// Accounts that only sign in with Google/Apple have no password to use here.
func (c Config) PasswordLogin(ctx context.Context, prompt Prompter) (*Token, error) {
	p, err := newPKCE()
	if err != nil {
		return nil, err
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{
		Jar:     jar,
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if strings.HasPrefix(req.URL.String(), c.RedirectURI) || len(via) > 10 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := get(ctx, client, c.authorizeURL(p))
	if err != nil {
		return nil, err
	}

	var sentPassword, sentCode, sentConsent bool
	for step := 0; step < 8; step++ {
		if loc := resp.Header.Get("Location"); resp.StatusCode/100 == 3 && strings.HasPrefix(loc, c.RedirectURI) {
			resp.Body.Close()
			code, err := c.codeFromCallback(loc, p)
			if err != nil {
				return nil, err
			}
			return c.exchangeCode(ctx, code, p)
		}
		page, err := readPage(resp)
		if err != nil {
			return nil, err
		}
		form := page.pick()
		if form == nil {
			return nil, fmt.Errorf("unexpected page from Prusa Account (%d %q)%s", page.status, page.title, page.errorsSuffix())
		}
		values := form.hidden
		switch form.kind {
		case formLogin:
			if sentPassword {
				return nil, fmt.Errorf("Prusa Account rejected the sign-in%s", page.errorsSuffix())
			}
			email, err := prompt.Email()
			if err != nil {
				return nil, err
			}
			password, err := prompt.Password()
			if err != nil {
				return nil, err
			}
			values.Set(form.userField, email)
			values.Set(form.passField, password)
			sentPassword = true
		case formOTP:
			if sentCode {
				return nil, fmt.Errorf("Prusa Account rejected the 2FA code%s", page.errorsSuffix())
			}
			code, err := prompt.OneTimeCode()
			if err != nil {
				return nil, err
			}
			values.Set(form.codeField, strings.ReplaceAll(code, " ", ""))
			sentCode = true
		case formConsent:
			if sentConsent {
				return nil, errors.New("Prusa Account kept asking to authorize the app")
			}
			values.Set("allow", "Authorize")
			sentConsent = true
		}
		if resp, err = postForm(ctx, client, page.url, form.action, values); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("sign-in did not finish; Prusa Account may have changed its login pages")
}

func get(ctx context.Context, client *http.Client, u string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/html")
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching Prusa Account: %w", err)
	}
	return resp, nil
}

func postForm(ctx context.Context, client *http.Client, page *url.URL, action string, values url.Values) (*http.Response, error) {
	target, err := page.Parse(action)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target.String(), strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "text/html")
	// Django accepts the csrftoken cookie as the form token or as X-CSRFToken,
	// so a page redesign that moves the hidden field doesn't break sign-in.
	if client.Jar != nil {
		for _, c := range client.Jar.Cookies(target) {
			if c.Name == "csrftoken" {
				req.Header.Set("X-CSRFToken", c.Value)
				if values.Get("csrfmiddlewaretoken") == "" {
					values.Set("csrfmiddlewaretoken", c.Value)
					req.Body = io.NopCloser(strings.NewReader(values.Encode()))
					req.ContentLength = int64(len(values.Encode()))
				}
			}
		}
	}
	// Django's CSRF protection requires a same-origin Referer over HTTPS.
	req.Header.Set("Referer", page.String())
	req.Header.Set("Origin", page.Scheme+"://"+page.Host)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("reaching Prusa Account: %w", err)
	}
	return resp, nil
}

type formKind int

const (
	formLogin formKind = iota + 1
	formOTP
	formConsent
)

type form struct {
	kind                            formKind
	action                          string
	hidden                          url.Values
	userField, passField, codeField string
}

type page struct {
	url    *url.URL
	status int
	title  string
	forms  []*form
	errors []string
}

func (p *page) pick() *form {
	for _, want := range []formKind{formLogin, formOTP, formConsent} {
		for _, f := range p.forms {
			if f.kind == want {
				return f
			}
		}
	}
	return nil
}

func (p *page) errorsSuffix() string {
	if len(p.errors) == 0 {
		return ""
	}
	return ": " + strings.Join(p.errors, "; ")
}

func readPage(resp *http.Response) (*page, error) {
	defer resp.Body.Close()
	root, err := html.Parse(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	p := &page{url: resp.Request.URL, status: resp.StatusCode}
	var walk func(n *html.Node, cur *form, fields *[]*html.Node)
	walk = func(n *html.Node, cur *form, fields *[]*html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "title":
				if n.FirstChild != nil && p.title == "" {
					p.title = strings.TrimSpace(n.FirstChild.Data)
				}
			case "form":
				if strings.EqualFold(attr(n, "method"), "post") {
					f := &form{action: attr(n, "action"), hidden: url.Values{}}
					var inputs []*html.Node
					for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
						walk(ch, f, &inputs)
					}
					classify(f, inputs)
					if f.kind != 0 {
						p.forms = append(p.forms, f)
					}
					return
				}
			case "input", "button":
				if fields != nil {
					*fields = append(*fields, n)
				}
			}
			cls := attr(n, "class")
			if strings.Contains(cls, "errorlist") || strings.Contains(cls, "alert-danger") || strings.Contains(cls, "invalid-feedback") {
				if t := strings.Join(strings.Fields(text(n)), " "); t != "" {
					p.errors = append(p.errors, t)
				}
			}
		}
		for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
			walk(ch, cur, fields)
		}
	}
	walk(root, nil, nil)
	return p, nil
}

func classify(f *form, inputs []*html.Node) {
	var textFields []string
	for _, in := range inputs {
		name, typ := attr(in, "name"), strings.ToLower(attr(in, "type"))
		if name == "" {
			continue
		}
		switch {
		case typ == "hidden":
			f.hidden.Set(name, attr(in, "value"))
		case typ == "password":
			f.passField = name
		case in.Data == "input" && (typ == "" || typ == "text" || typ == "email" || typ == "number" || typ == "tel"):
			textFields = append(textFields, name)
		case in.Data == "button" || typ == "submit":
			if name == "allow" {
				f.kind = formConsent
			}
		}
	}
	switch {
	case f.passField != "":
		f.kind = formLogin
		f.userField = "email"
		for _, n := range textFields {
			if strings.Contains(n, "email") || strings.Contains(n, "user") || strings.Contains(n, "login") {
				f.userField = n
			}
		}
	case f.kind == formConsent:
	default:
		for _, n := range textFields {
			l := strings.ToLower(n)
			if strings.Contains(l, "otp") || strings.Contains(l, "token") || strings.Contains(l, "code") {
				f.kind, f.codeField = formOTP, n
			}
		}
		if f.kind == 0 && f.hidden.Get("client_id") != "" && f.hidden.Get("redirect_uri") != "" {
			f.kind = formConsent
		}
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func text(n *html.Node) string {
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for ch := n.FirstChild; ch != nil; ch = ch.NextSibling {
		b.WriteString(text(ch))
		b.WriteByte(' ')
	}
	return b.String()
}
