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

// User is the subset of a GitLab user blastdoor reads.
type User struct {
	ID int `json:"id"`
}

// MergeRequest is the subset of fields blastdoor reads.
type MergeRequest struct {
	IID       int    `json:"iid"`
	Author    User   `json:"author"`
	Reviewers []User `json:"reviewers"`
}

// GetMergeRequest reads one merge request.
func (c *Client) GetMergeRequest(ctx context.Context, iid int) (MergeRequest, error) {
	var mr MergeRequest
	if err := c.do(ctx, http.MethodGet, c.mrPath(iid, ""), nil, &mr); err != nil {
		return MergeRequest{}, err
	}
	return mr, nil
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

// GroupMember is the subset of a group membership blastdoor reads.
type GroupMember struct {
	ID    int    `json:"id"`
	State string `json:"state"`
}

// GroupMembers returns the ids of a group's active direct members.
//
// Direct members, not /members/all: inherited membership walks up the group
// hierarchy, so naming one team as an approver could quietly put the whole
// organisation on a merge request. Blocked and pending members are dropped —
// they cannot review, and GitLab rejects an update naming one.
func (c *Client) GroupMembers(ctx context.Context, groupID int) ([]int, error) {
	const perPage = 100

	var ids []int
	for page := 1; ; page++ {
		q := url.Values{}
		q.Set("per_page", strconv.Itoa(perPage))
		q.Set("page", strconv.Itoa(page))

		var members []GroupMember
		path := fmt.Sprintf("/groups/%d/members?%s", groupID, q.Encode())
		if err := c.do(ctx, http.MethodGet, path, nil, &members); err != nil {
			return nil, fmt.Errorf("reading members of group %d: %w", groupID, err)
		}

		for _, m := range members {
			if m.State != "" && m.State != "active" {
				continue
			}
			ids = append(ids, m.ID)
		}

		// A short page is the last one.
		if len(members) < perPage {
			return ids, nil
		}
	}
}

// AddReviewers puts users on the merge request's reviewer list and returns the
// ones it actually added.
//
// GitLab's reviewer_ids replaces the list rather than appending to it, so the
// current reviewers are read first and kept: somebody who put themselves on a
// merge request must not be dropped by the next pipeline. The author is
// skipped — a person does not review their own change.
func (c *Client) AddReviewers(ctx context.Context, iid int, userIDs []int) ([]int, error) {
	mr, err := c.GetMergeRequest(ctx, iid)
	if err != nil {
		return nil, err
	}

	have := make(map[int]bool, len(mr.Reviewers))
	all := make([]int, 0, len(mr.Reviewers)+len(userIDs))
	for _, r := range mr.Reviewers {
		have[r.ID] = true
		all = append(all, r.ID)
	}

	var added []int
	for _, id := range userIDs {
		if have[id] || id == mr.Author.ID {
			continue
		}
		have[id] = true
		added = append(added, id)
		all = append(all, id)
	}
	if len(added) == 0 {
		return nil, nil
	}

	if err := c.do(ctx, http.MethodPut, c.mrPath(iid, ""), map[string]any{"reviewer_ids": all}, nil); err != nil {
		return nil, err
	}
	return added, nil
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
