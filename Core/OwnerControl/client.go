package ownercontrol

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/Core/Invitation"
	"github.com/Neon-Dolls/neondoll/Core/PermissionRequest"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

type Client struct{ http *http.Client }

func NewClient(socketPath string) *Client {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
	}}
	return &Client{http: &http.Client{Transport: transport, Timeout: 10 * time.Second}}
}

func (c *Client) ListPending(ctx context.Context) ([]*enrollment.PendingEnrollment, error) {
	var result []*enrollment.PendingEnrollment
	return result, c.do(ctx, http.MethodGet, "/v1/pending", nil, &result)
}
func (c *Client) Approve(ctx context.Context, code string, permissions []string) (*dollnetwork.EnrollmentReceipt, error) {
	var result dollnetwork.EnrollmentReceipt
	return &result, c.do(ctx, http.MethodPost, "/v1/approve", approveRequest{Code: code, Permissions: permissions}, &result)
}
func (c *Client) Deny(ctx context.Context, code string) error {
	return c.do(ctx, http.MethodPost, "/v1/deny", denyRequest{Code: code}, nil)
}
func (c *Client) CreateInvitation(ctx context.Context, lifetime time.Duration, permissions []string, endpoints dollnetwork.Endpoints) (*invitation.CreatedInvitation, error) {
	var result invitation.CreatedInvitation
	err := c.do(ctx, http.MethodPost, "/v1/invitations", invitationRequest{LifetimeSeconds: int64(lifetime / time.Second), Permissions: permissions, Endpoints: endpoints}, &result)
	return &result, err
}
func (c *Client) ListBodies(ctx context.Context) ([]*enrollment.BodyRecord, error) {
	var result []*enrollment.BodyRecord
	return result, c.do(ctx, http.MethodGet, "/v1/bodies", nil, &result)
}
func (c *Client) Revoke(ctx context.Context, bodyID string) error {
	return c.do(ctx, http.MethodPost, "/v1/revoke", revokeRequest{BodyID: bodyID}, nil)
}

func (c *Client) ListPermissionRequests(ctx context.Context) ([]*permissionrequest.Request, error) {
	var result []*permissionrequest.Request
	return result, c.do(ctx, http.MethodGet, "/v1/permission-requests", nil, &result)
}

func (c *Client) GetPermissionRequest(ctx context.Context, id permissionrequest.ID) (*permissionrequest.Request, error) {
	var result permissionrequest.Request
	err := c.do(ctx, http.MethodGet, "/v1/permission-requests/"+string(id), nil, &result)
	return &result, err
}

func (c *Client) do(ctx context.Context, method, path string, body, result any) error {
	var encoded []byte
	var err error
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://unix"+path, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("owner control: %s", response.Status)
	}
	if result == nil || response.StatusCode == http.StatusNoContent {
		return nil
	}
	return json.NewDecoder(response.Body).Decode(result)
}
