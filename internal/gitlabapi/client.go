// Package gitlabapi is a small GitLab REST client covering the endpoints
// blastdoor's gate needs: merge request lookup, notes, approval rules,
// approval and merge.
package gitlabapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Client talks to one GitLab project.
type Client struct {
	BaseURL   string // e.g. https://gitlab.example.com/api/v4
	ProjectID string
	Token     string
	HTTP      *http.Client
}

// New builds a client with a sensible timeout.
func New(baseURL, projectID, token string) *Client {
	return &Client{
		BaseURL:   strings.TrimSuffix(baseURL, "/"),
		ProjectID: projectID,
		Token:     token,
		HTTP:      &http.Client{Timeout: 30 * time.Second},
	}
}

// AuthError reports a 401/403, which for blastdoor means the token lacks the
// api scope. The gate treats it as fatal rather than assuming no gate is
// needed.
type AuthError struct {
	Status int
	Body   string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("GitLab returned %d — the token needs the 'api' scope: %s", e.Status, e.Body)
}

// APIError is any other non-2xx response.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("GitLab returned %d: %s", e.Status, e.Body)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request: %w", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("calling GitLab: %w", err)
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(resp.Body)
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return &AuthError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	case resp.StatusCode >= 400:
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}

	if out != nil {
		if err := json.Unmarshal(raw, out); err != nil {
			return fmt.Errorf("decoding response: %w", err)
		}
	}
	return nil
}

func (c *Client) mrPath(iid int, suffix string) string {
	return fmt.Sprintf("/projects/%s/merge_requests/%d%s", url.PathEscape(c.ProjectID), iid, suffix)
}

// MergeRequest is the subset of fields blastdoor reads.
type MergeRequest struct {
	IID int `json:"iid"`
}

// FindMergeRequestForBranch returns the highest open MR IID for a source
// branch, for pipelines that run on a branch rather than on the MR itself.
// It returns 0 when the branch has no open MR.
func (c *Client) FindMergeRequestForBranch(ctx context.Context, branch string) (int, error) {
	q := url.Values{}
	q.Set("source_branch", branch)
	q.Set("state", "opened")
	path := fmt.Sprintf("/projects/%s/merge_requests?%s", url.PathEscape(c.ProjectID), q.Encode())

	var mrs []MergeRequest
	if err := c.do(ctx, http.MethodGet, path, nil, &mrs); err != nil {
		return 0, err
	}

	best := 0
	for _, mr := range mrs {
		if mr.IID > best {
			best = mr.IID
		}
	}
	return best, nil
}

// PostNote comments on a merge request.
func (c *Client) PostNote(ctx context.Context, iid int, body string) error {
	return c.do(ctx, http.MethodPost, c.mrPath(iid, "/notes"), map[string]string{"body": body}, nil)
}

// ApprovalRule is a merge-request-level approval rule.
type ApprovalRule struct {
	ID                int    `json:"id"`
	Name              string `json:"name"`
	ApprovalsRequired int    `json:"approvals_required"`
}

// ListApprovalRules returns the merge request's approval rules.
func (c *Client) ListApprovalRules(ctx context.Context, iid int) ([]ApprovalRule, error) {
	var rules []ApprovalRule
	if err := c.do(ctx, http.MethodGet, c.mrPath(iid, "/approval_rules"), nil, &rules); err != nil {
		return nil, err
	}
	return rules, nil
}

// SetApprovalRule creates or updates a named rule, so re-running a pipeline
// does not pile up duplicates. groupIDs may be empty.
func (c *Client) SetApprovalRule(ctx context.Context, iid int, name string, approvalsRequired int, groupIDs []int) error {
	rules, err := c.ListApprovalRules(ctx, iid)
	if err != nil {
		return err
	}

	body := map[string]any{
		"name":               name,
		"approvals_required": approvalsRequired,
		"rule_type":          "regular",
	}
	if len(groupIDs) > 0 {
		body["group_ids"] = groupIDs
	}

	for _, r := range rules {
		if r.Name == name {
			return c.do(ctx, http.MethodPut, c.mrPath(iid, "/approval_rules/"+strconv.Itoa(r.ID)), body, nil)
		}
	}
	return c.do(ctx, http.MethodPost, c.mrPath(iid, "/approval_rules"), body, nil)
}

// Approve approves the merge request as the token's user. GitLab answers 401
// when that user has already approved, which is not a failure for us.
func (c *Client) Approve(ctx context.Context, iid int) error {
	err := c.do(ctx, http.MethodPost, c.mrPath(iid, "/approve"), nil, nil)
	var authErr *AuthError
	if errors.As(err, &authErr) && authErr.Status == http.StatusUnauthorized {
		return nil
	}
	return err
}

// Unapprove withdraws the token user's approval.
//
// This matters when a merge request gets riskier: an approval blastdoor gave
// an earlier, safe push would otherwise still satisfy the rule it raises now.
// GitLab answers 404 when there is no approval to withdraw, which is fine.
func (c *Client) Unapprove(ctx context.Context, iid int) error {
	err := c.do(ctx, http.MethodPost, c.mrPath(iid, "/unapprove"), nil, nil)

	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound {
		return nil
	}
	var authErr *AuthError
	if errors.As(err, &authErr) && authErr.Status == http.StatusUnauthorized {
		return nil
	}
	return err
}

// MergeWhenPipelineSucceeds queues the merge for when the pipeline goes green.
func (c *Client) MergeWhenPipelineSucceeds(ctx context.Context, iid int, squash bool) error {
	body := map[string]any{
		"merge_when_pipeline_succeeds": true,
		"squash":                       squash,
	}
	return c.do(ctx, http.MethodPut, c.mrPath(iid, "/merge"), body, nil)
}
