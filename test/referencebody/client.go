package referencebody

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"

	"github.com/Neon-Dolls/neondoll/DollLink/Authentication"
	"github.com/Neon-Dolls/neondoll/DollLink/Capabilities"
	"github.com/Neon-Dolls/neondoll/DollLink/Execution"
	ws "github.com/Neon-Dolls/neondoll/DollLink/WebSocket"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

type Logger struct{}

func (Logger) Info(string, ...map[string]any)  {}
func (Logger) Warn(string, ...map[string]any)  {}
func (Logger) Error(string, ...map[string]any) {}

type Client struct {
	StatePath string
	State     *State
	HTTP      *http.Client
}

func New(statePath string) (*Client, error) {
	state, err := LoadOrCreate(statePath)
	if err != nil {
		return nil, err
	}
	return &Client{StatePath: statePath, State: state, HTTP: &http.Client{Timeout: 5 * time.Second}}, nil
}

func (c *Client) BeginEnrollment(ctx context.Context, baseURL, coreID string, corePublicKey ed25519.PublicKey) (*dollnetwork.EnrollmentPendingResponse, error) {
	if c.State.Receipt != nil {
		return nil, errors.New("reference Body: already enrolled")
	}
	privateKey, err := c.State.SigningPrivate()
	if err != nil {
		return nil, err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	wg, err := c.State.WireGuardKeypair()
	if err != nil {
		return nil, err
	}
	if c.State.RequestID == "" {
		c.State.RequestID = "request_" + uuid.NewString()
	}
	c.State.CoreID = coreID
	c.State.CorePublicKey = base64.RawURLEncoding.EncodeToString(corePublicKey)
	if err := c.State.Save(c.StatePath); err != nil {
		return nil, err
	}
	bodyNonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	request := dollnetwork.EnrollmentBeginRequest{
		Version: 1, RequestID: c.State.RequestID, CoreID: coreID,
		BodyPublicKey: dollnetwork.EncodeRawURL(publicKey), WGPublicKey: wg.PublicKeyBase64(),
		BodyNonce: bodyNonce, Metadata: c.State.Metadata,
	}
	if err := dollnetwork.SignEnrollmentBegin(privateKey, &request); err != nil {
		return nil, err
	}
	var pending dollnetwork.EnrollmentPendingResponse
	if err := c.post(ctx, baseURL+"/v1/body/enrollment/begin", request, &pending); err != nil {
		return nil, err
	}
	if err := dollnetwork.VerifyEnrollmentPending(corePublicKey, pending); err != nil {
		return nil, fmt.Errorf("reference Body: verify pending response: %w", err)
	}
	if pending.Version != dollnetwork.EnrollmentVersion || pending.CoreID != coreID || pending.RequestID != c.State.RequestID ||
		pending.BodyKeyFingerprint != dollnetwork.BodyKeyFingerprint(publicKey) || pending.ApprovalCode == "" || !time.Now().Before(pending.ExpiresAt) {
		return nil, errors.New("reference Body: pending response binding mismatch")
	}
	return &pending, nil
}

func (c *Client) PollEnrollment(ctx context.Context, baseURL string) (*dollnetwork.EnrollmentReceipt, error) {
	privateKey, err := c.State.SigningPrivate()
	if err != nil {
		return nil, err
	}
	publicKey := privateKey.Public().(ed25519.PublicKey)
	corePublicKey, err := c.State.CorePublic()
	if err != nil {
		return nil, err
	}
	bodyNonce, err := newNonce()
	if err != nil {
		return nil, err
	}
	request := dollnetwork.EnrollmentResultRequest{
		Version: 1, RequestID: c.State.RequestID, CoreID: c.State.CoreID,
		BodyPublicKey: dollnetwork.EncodeRawURL(publicKey), BodyNonce: bodyNonce,
	}
	if err := dollnetwork.SignEnrollmentResult(privateKey, &request); err != nil {
		return nil, err
	}
	var result dollnetwork.EnrollmentResultResponse
	if err := c.post(ctx, baseURL+"/v1/body/enrollment/result", request, &result); err != nil {
		return nil, err
	}
	if result.Status != dollnetwork.EnrollmentStatusEnrolled || result.Receipt == nil {
		return nil, fmt.Errorf("reference Body: enrollment status %q", result.Status)
	}
	receipt := result.Receipt
	if err := dollnetwork.VerifyEnrollmentReceipt(corePublicKey, *receipt); err != nil {
		return nil, fmt.Errorf("reference Body: verify receipt: %w", err)
	}
	if receipt.Version != dollnetwork.EnrollmentVersion || receipt.Status != dollnetwork.EnrollmentStatusEnrolled || receipt.BodyID == "" || receipt.MembershipReference == "" ||
		receipt.CoreID != c.State.CoreID || receipt.RequestID != c.State.RequestID || receipt.BodyPublicKeyFingerprint != dollnetwork.BodyKeyFingerprint(publicKey) {
		return nil, errors.New("reference Body: receipt binding mismatch")
	}
	c.State.Receipt = receipt
	if err := c.State.Save(c.StatePath); err != nil {
		return nil, err
	}
	return receipt, nil
}

func (c *Client) Connect(ctx context.Context, websocketURL string, connected func(string)) error {
	if c.State.Receipt == nil {
		return errors.New("reference Body: enrollment receipt is required")
	}
	privateKey, err := c.State.SigningPrivate()
	if err != nil {
		return err
	}
	corePublicKey, err := c.State.CorePublic()
	if err != nil {
		return err
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, websocketURL, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	bodyNonce, err := newNonce()
	if err != nil {
		return err
	}
	hello := authentication.Hello{Version: authentication.Version, Type: authentication.TypeHello, BodyID: c.State.BodyID(), BodyNonce: bodyNonce, Algorithms: []string{authentication.AlgorithmEd25519}}
	if err := conn.WriteJSON(hello); err != nil {
		return err
	}
	var challenge authentication.Challenge
	if err := conn.ReadJSON(&challenge); err != nil {
		return err
	}
	if err := authentication.VerifyChallenge(corePublicKey, challenge); err != nil {
		return fmt.Errorf("reference Body: verify challenge: %w", err)
	}
	if challenge.Version != authentication.Version || challenge.Type != authentication.TypeChallenge || challenge.Algorithm != authentication.AlgorithmEd25519 ||
		challenge.CoreID != c.State.CoreID || challenge.BodyID != c.State.BodyID() || challenge.BodyNonce != bodyNonce || !time.Now().Before(challenge.ExpiresAt) {
		return errors.New("reference Body: challenge binding mismatch")
	}
	proof := authentication.Proof{
		Version: 1, Type: authentication.TypeProof, CoreID: challenge.CoreID, BodyID: challenge.BodyID,
		SessionID: challenge.SessionID, BodyNonce: challenge.BodyNonce, CoreNonce: challenge.CoreNonce, Algorithm: challenge.Algorithm,
	}
	if err := authentication.SignProof(privateKey, &proof); err != nil {
		return err
	}
	if err := conn.WriteJSON(proof); err != nil {
		return err
	}
	var accept authentication.Accept
	if err := conn.ReadJSON(&accept); err != nil {
		return err
	}
	if err := authentication.VerifyAccept(corePublicKey, accept); err != nil {
		return fmt.Errorf("reference Body: verify accept: %w", err)
	}
	if accept.Version != authentication.Version || accept.Type != authentication.TypeAccept || accept.CoreID != c.State.CoreID ||
		accept.BodyID != c.State.BodyID() || accept.SessionID != challenge.SessionID || accept.Status != "authenticated" {
		return errors.New("reference Body: authentication acceptance mismatch")
	}
	dispatcher, err := execution.NewDispatcher(c.State.BodyID(), execution.LocalPermissionFunc(func(_ context.Context, request execution.Request) bool {
		return request.Payload.Capability == "test.echo" && request.Payload.Operation == "echo"
	}), execution.CapabilityExecutorFunc(func(_ context.Context, capability, operation string, arguments map[string]any) (map[string]any, error) {
		if capability != "test.echo" || operation != "echo" {
			return nil, execution.ErrUnsupported
		}
		return map[string]any{"echo": arguments["message"]}, nil
	}))
	if err != nil {
		return err
	}
	bodyClient, err := ws.NewClient(conn, ws.ClientConfig{AuthenticatedExecutionHandler: dispatcher}, Logger{})
	if err != nil {
		return err
	}
	if err := bodyClient.AdvertiseCapabilities(ctx, c.State.BodyID(), []capabilities.Capability{{ID: "test.echo", Operations: []string{"echo"}, Available: true}}); err != nil {
		return err
	}
	if connected != nil {
		connected(c.State.BodyID())
	}
	return bodyClient.Run(ctx)
}

func (c *Client) post(ctx context.Context, url string, input, output any) error {
	encoded, err := json.Marshal(input)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.HTTP.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("reference Body: %s returned %s", url, response.Status)
	}
	return json.NewDecoder(response.Body).Decode(output)
}

func newNonce() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return dollnetwork.EncodeRawURL(value), nil
}
