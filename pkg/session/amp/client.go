// Package amp delivers messages to existing Amp Orb threads over HTTP.
package amp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

const ThreadURLPrefix = "https://ampcode.com/threads/"

var threadID = regexp.MustCompile(`^T-[A-Za-z0-9]+(-[A-Za-z0-9]+)*$`)

func ParseThreadRef(ref string) (string, error) {
	id := strings.TrimPrefix(strings.TrimSpace(ref), ThreadURLPrefix)
	if len(id) > 128 || !threadID.MatchString(id) {
		return "", fmt.Errorf("enter an Amp thread ID (T-…) or its ampcode.com thread URL")
	}
	return id, nil
}

type Thread struct {
	ID           string       `json:"id"`
	Title        string       `json:"title"`
	CreatorID    string       `json:"creatorUserID"`
	Archived     bool         `json:"archived"`
	Hostname     string       `json:"hostname"`
	Repositories []Repository `json:"repositories"`
}

type Repository struct {
	URL string `json:"url"`
}

// CanReceive excludes archived threads and known local and runner executors.
// Amp checks Orb eligibility when it accepts the POST.
func (t Thread) CanReceive() bool { return !t.Archived && t.Hostname == "" }

type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

func New(token string) *Client {
	return &Client{
		baseURL: "https://ampcode.com",
		token:   strings.TrimSpace(token),
		http: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		}},
	}
}

// List returns the authenticated account's threads in Amp's most-recent-first order.
func (c *Client) List(ctx context.Context) ([]Thread, error) {
	var threads []Thread
	seen := map[string]bool{}
	cursor := ""
	for {
		query := url.Values{"limit": {"100"}}
		if cursor != "" {
			query.Set("cursor", cursor)
		}
		var page struct {
			Threads []Thread `json:"threads"`
			Next    string   `json:"nextCursor"`
		}
		if err := c.request(ctx, http.MethodGet, "/api/v2/user/threads?"+query.Encode(), "", http.StatusOK, &page); err != nil {
			return nil, fmt.Errorf("list Amp threads: %w", err)
		}
		if page.Threads == nil || seen[page.Next] {
			return nil, fmt.Errorf("list Amp threads: invalid pagination response")
		}
		threads = append(threads, page.Threads...)
		if page.Next == "" {
			return threads, nil
		}
		cursor, seen[page.Next] = page.Next, true
	}
}

// OwnedOrb checks whether the current account can use a thread as an Orb target.
func (c *Client) OwnedOrb(ctx context.Context, ref string) (Thread, error) {
	id, err := ParseThreadRef(ref)
	if err != nil {
		return Thread{}, err
	}
	var identity struct {
		Actor struct{ ID string } `json:"actor"`
	}
	if err := c.request(ctx, http.MethodGet, "/api/v2/actor", "", http.StatusOK, &identity); err != nil {
		return Thread{}, fmt.Errorf("verify Amp account: %w", err)
	}
	var thread Thread
	if err := c.request(ctx, http.MethodGet, "/api/v2/threads/"+id+"/metadata", "", http.StatusOK, &thread); err != nil {
		return Thread{}, fmt.Errorf("look up Amp thread %s: %w", id, err)
	}
	switch {
	case identity.Actor.ID == "" || thread.ID != id:
		return Thread{}, fmt.Errorf("invalid account or thread metadata from Amp for %s", id)
	case thread.CreatorID != identity.Actor.ID:
		return Thread{}, fmt.Errorf("the Amp account does not own thread %s", id)
	case thread.Archived:
		return Thread{}, fmt.Errorf("thread %s is archived in Amp", id)
	case !thread.CanReceive():
		return Thread{}, fmt.Errorf("delivery requires an Amp Orb thread, but thread %s is local or on a runner", id)
	}
	return thread, nil
}

// Send returns the accepted message ID. Amp's create-message API has no
// idempotency key, so a failed or unconfirmed submission is never replayed here.
func (c *Client) Send(ctx context.Context, ref, message string) (string, error) {
	id, err := ParseThreadRef(ref)
	if err != nil {
		return "", err
	}
	var receipt struct {
		Thread    Thread `json:"thread"`
		MessageID string `json:"messageID"`
	}
	if err := c.request(ctx, http.MethodPost, "/api/v2/threads/"+id+"/messages", message, http.StatusAccepted, &receipt); err != nil {
		return "", fmt.Errorf("send to Amp thread %s: %w", id, err)
	}
	if receipt.Thread.ID != id || strings.TrimSpace(receipt.MessageID) == "" {
		return "", fmt.Errorf("delivery to Amp thread %s is unconfirmed: invalid message receipt", id)
	}
	return receipt.MessageID, nil
}

func (c *Client) request(ctx context.Context, method, path, message string, expected int, out any) error {
	if c.token == "" {
		return fmt.Errorf("set AMP_API_KEY to an Amp personal access token")
	}
	var body io.Reader
	if method == http.MethodPost {
		payload, err := json.Marshal(map[string]string{"message": message})
		if err != nil {
			return err
		}
		body = strings.NewReader(string(payload))
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("unconfirmed Amp HTTP response: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != expected {
		return responseError(res.StatusCode, method == http.MethodPost)
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 8<<20)).Decode(out); err != nil {
		return fmt.Errorf("unconfirmed Amp HTTP response: invalid JSON")
	}
	return nil
}

func responseError(status int, sending bool) error {
	switch status {
	case http.StatusUnauthorized:
		return fmt.Errorf("personal access token rejected by Amp (HTTP %d)", status)
	case http.StatusPaymentRequired:
		return fmt.Errorf("the Amp account is out of credits (HTTP %d)", status)
	case http.StatusForbidden:
		return fmt.Errorf("access denied by Amp (HTTP %d). Check the token's thread permissions and workspace policy", status)
	case http.StatusNotFound:
		return fmt.Errorf("no accessible, owned Orb thread found in Amp (HTTP %d)", status)
	case http.StatusConflict:
		return fmt.Errorf("thread is archived in Amp (HTTP %d)", status)
	case http.StatusTooManyRequests:
		return fmt.Errorf("rate limit reached in Amp (HTTP %d)", status)
	default:
		if sending && status >= http.StatusInternalServerError {
			return fmt.Errorf("unconfirmed Amp delivery (HTTP %d)", status)
		}
		return fmt.Errorf("unexpected HTTP %d from Amp", status)
	}
}
