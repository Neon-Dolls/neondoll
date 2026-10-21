package enrollment_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Neon-Dolls/neondoll/Core/Enrollment"
	"github.com/Neon-Dolls/neondoll/DollNetwork"
)

func TestCoreIdentityDiscoveryReturnsOnlyPersistentPublicIdentity(t *testing.T) {
	fx := newFixture(t, filepath.Join(t.TempDir(), "core.db"))
	recorder := httptest.NewRecorder()
	enrollment.NewHandler(fx.service).CoreIdentity(recorder, httptest.NewRequest(http.MethodGet, "/v1/core/identity", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response dollnetwork.CoreIdentityResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	want := fx.service.CoreIdentity()
	if response.Version != dollnetwork.CoreIdentityVersion || response.CoreID != want.CoreID ||
		response.Algorithm != dollnetwork.CoreIdentityAlgorithm || response.PublicKey != dollnetwork.EncodeRawURL(want.PublicKey) || strings.Contains(response.PublicKey, "=") {
		t.Fatalf("identity response = %#v", response)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(recorder.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 4 || fields["private_key"] != nil || strings.Contains(strings.ToLower(recorder.Body.String()), "private") {
		t.Fatalf("identity response exposed unexpected fields: %s", recorder.Body.String())
	}

	methodRecorder := httptest.NewRecorder()
	enrollment.NewHandler(fx.service).CoreIdentity(methodRecorder, httptest.NewRequest(http.MethodPost, "/v1/core/identity", nil))
	if methodRecorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST status = %d", methodRecorder.Code)
	}
}
