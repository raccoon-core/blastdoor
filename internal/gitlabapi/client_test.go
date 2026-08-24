package gitlabapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recorded struct {
	method string
	path   string
	body   map[string]any
}

// server records the requests it receives and replies from handler.
func server(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*Client, *[]recorded) {
	t.Helper()
	var got []recorded

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{method: r.Method, path: r.URL.RequestURI()}
		if raw, _ := io.ReadAll(r.Body); len(raw) > 0 {
			_ = json.Unmarshal(raw, &rec.body)
		}
		got = append(got, rec)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	return New(srv.URL, "42", "token"), &got
}

func TestPostNote(t *testing.T) {
	client, got := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	})

	if err := client.PostNote(context.Background(), 7, "hello"); err != nil {
		t.Fatalf("PostNote: %v", err)
	}

	if len(*got) != 1 {
		t.Fatalf("got %d requests, want 1", len(*got))
	}
	if (*got)[0].path != "/projects/42/merge_requests/7/notes" {
		t.Errorf("path = %q", (*got)[0].path)
	}
	if (*got)[0].body["body"] != "hello" {
		t.Errorf("body = %v", (*got)[0].body)
	}
}

// A 401/403 must surface as AuthError so the gate can fail loudly instead of
// treating an unusable token as "no gate needed".
func TestAuthErrorOnForbidden(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"403 Forbidden"}`))
	})

	err := client.PostNote(context.Background(), 7, "hello")

	var authErr *AuthError
	if !errors.As(err, &authErr) {
		t.Fatalf("got %T (%v), want *AuthError", err, err)
	}
	if authErr.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", authErr.Status)
	}
}

func TestAPIErrorOnServerError(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})

	var apiErr *APIError
	if err := client.PostNote(context.Background(), 7, "x"); !errors.As(err, &apiErr) {
		t.Fatalf("got %T (%v), want *APIError", err, err)
	}
}

// Re-running a pipeline must update the existing rule, not add another.
func TestSetApprovalRuleUpdatesExisting(t *testing.T) {
	client, got := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"id":9,"name":"blastdoor","approvals_required":1}]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})

	if err := client.SetApprovalRule(context.Background(), 7, "blastdoor", 1, []int{5}); err != nil {
		t.Fatalf("SetApprovalRule: %v", err)
	}

	if len(*got) != 2 {
		t.Fatalf("got %d requests, want 2", len(*got))
	}
	write := (*got)[1]
	if write.method != http.MethodPut {
		t.Errorf("method = %s, want PUT (the rule already exists)", write.method)
	}
	if write.path != "/projects/42/merge_requests/7/approval_rules/9" {
		t.Errorf("path = %q", write.path)
	}
}

func TestSetApprovalRuleCreatesWhenAbsent(t *testing.T) {
	client, got := server(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`[{"id":3,"name":"some-other-rule"}]`))
			return
		}
		_, _ = w.Write([]byte(`{}`))
	})

	if err := client.SetApprovalRule(context.Background(), 7, "blastdoor", 1, nil); err != nil {
		t.Fatalf("SetApprovalRule: %v", err)
	}

	write := (*got)[1]
	if write.method != http.MethodPost {
		t.Errorf("method = %s, want POST", write.method)
	}
	if _, ok := write.body["group_ids"]; ok {
		t.Errorf("group_ids should be omitted when no groups are given: %v", write.body)
	}
}

// GitLab answers 401 when the token's user has already approved. That is not
// a failure.
func TestApproveTreatsAlreadyApprovedAsSuccess(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"401 Unauthorized"}`))
	})

	if err := client.Approve(context.Background(), 7); err != nil {
		t.Errorf("Approve: %v, want nil", err)
	}
}

func TestFindMergeRequestForBranchPicksHighestIID(t *testing.T) {
	client, got := server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[{"iid":3},{"iid":11},{"iid":7}]`))
	})

	iid, err := client.FindMergeRequestForBranch(context.Background(), "feature/x")
	if err != nil {
		t.Fatalf("FindMergeRequestForBranch: %v", err)
	}
	if iid != 11 {
		t.Errorf("iid = %d, want 11", iid)
	}
	if (*got)[0].path != "/projects/42/merge_requests?source_branch=feature%2Fx&state=opened" {
		t.Errorf("path = %q", (*got)[0].path)
	}
}

func TestFindMergeRequestForBranchWithNoMatch(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	})

	iid, err := client.FindMergeRequestForBranch(context.Background(), "feature/x")
	if err != nil {
		t.Fatalf("FindMergeRequestForBranch: %v", err)
	}
	if iid != 0 {
		t.Errorf("iid = %d, want 0", iid)
	}
}

func TestMergeWhenPipelineSucceeds(t *testing.T) {
	client, got := server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})

	if err := client.MergeWhenPipelineSucceeds(context.Background(), 7, true); err != nil {
		t.Fatalf("MergeWhenPipelineSucceeds: %v", err)
	}

	req := (*got)[0]
	if req.method != http.MethodPut || req.path != "/projects/42/merge_requests/7/merge" {
		t.Errorf("%s %s", req.method, req.path)
	}
	if req.body["merge_when_pipeline_succeeds"] != true || req.body["squash"] != true {
		t.Errorf("body = %v", req.body)
	}
}

// Raising the gate must withdraw the approval blastdoor gave when the merge
// request was still under the threshold.
func TestUnapprove(t *testing.T) {
	client, got := server(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	})

	if err := client.Unapprove(context.Background(), 7); err != nil {
		t.Fatalf("Unapprove: %v", err)
	}

	req := (*got)[0]
	if req.method != http.MethodPost || req.path != "/projects/42/merge_requests/7/unapprove" {
		t.Errorf("%s %s", req.method, req.path)
	}
}

// There may be no approval to withdraw, which is not a failure.
func TestUnapproveToleratesNoExistingApproval(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusUnauthorized} {
		client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"message":"nope"}`))
		})
		if err := client.Unapprove(context.Background(), 7); err != nil {
			t.Errorf("status %d: %v, want nil", status, err)
		}
	}
}

// A 403 means the token cannot act, which must not pass silently.
func TestUnapproveStillFailsOnForbidden(t *testing.T) {
	client, _ := server(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	})
	if err := client.Unapprove(context.Background(), 7); err == nil {
		t.Error("a 403 was swallowed")
	}
}
