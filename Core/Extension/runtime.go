package extension

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

const maxGuestEvent = 4096
const maxHTTPResponse = 4 << 20

type GuestMessage struct {
	Version uint32          `json:"v"`
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

type instanceRuntime struct {
	manifest   Manifest
	module     api.Module
	ctx        context.Context
	cancel     context.CancelFunc
	eventPtr   uint32
	callMu     sync.Mutex
	outboundMu sync.Mutex
	outbound   []GuestMessage
	secrets    map[string][]byte
}

type WASMRuntime struct {
	mu          sync.Mutex
	runtime     wazero.Runtime
	state       StateStore
	secretsRoot string
	instances   map[string]*instanceRuntime
	closed      bool
}

func NewWASMRuntime(ctx context.Context, state StateStore, secretsRoot string) (*WASMRuntime, error) {
	if state == nil {
		return nil, ErrInvalidManifest
	}
	rt := wazero.NewRuntimeWithConfig(ctx, wazero.NewRuntimeConfig().WithMemoryLimitPages(1024).WithCloseOnContextDone(true))
	if _, err := wasi_snapshot_preview1.Instantiate(ctx, rt); err != nil {
		return nil, err
	}
	r := &WASMRuntime{runtime: rt, state: state, secretsRoot: secretsRoot, instances: map[string]*instanceRuntime{}}
	if err := r.installHost(ctx); err != nil {
		rt.Close(ctx)
		return nil, err
	}
	return r, nil
}

func (r *WASMRuntime) installHost(ctx context.Context) error {
	b := r.runtime.NewHostModuleBuilder("gateway_host")
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, p, n uint32) {
		if i := r.instance(m); i != nil && i.manifest.GrantedCapability(CapabilityLog) {
			_, _ = m.Memory().Read(p, n)
		}
	}).Export("log")
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, p, n uint32) {
		i := r.instance(m)
		if i == nil {
			return
		}
		raw, ok := m.Memory().Read(p, n)
		if !ok {
			return
		}
		var payload json.RawMessage = append([]byte(nil), raw...)
		i.outboundMu.Lock()
		i.outbound = append(i.outbound, GuestMessage{Version: 0, Type: "outgoing", Payload: payload})
		i.outboundMu.Unlock()
	}).Export("outgoing")
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, p, max uint32) int32 {
		i := r.instance(m)
		if i == nil {
			return 0
		}
		data := i.manifest.Configuration
		if uint32(len(data)) > max || !m.Memory().Write(p, data) {
			return 0
		}
		return int32(len(data))
	}).Export("config_projection")
	b.NewFunctionBuilder().WithFunc(func(_ context.Context, m api.Module, p, max uint32) int32 {
		return r.writeSecret(m, "telegram_token", p, max)
	}).Export("telegram_token")
	b.NewFunctionBuilder().WithFunc(r.httpGet).Export("http_get")
	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kp, kn, vp, vmax uint32) int32 {
		return r.stateGet(ctx, m, kp, kn, vp, vmax)
	}).Export("state_get")
	b.NewFunctionBuilder().WithFunc(func(ctx context.Context, m api.Module, kp, kn, vp, vn uint32) int32 {
		return r.statePut(ctx, m, kp, kn, vp, vn)
	}).Export("state_put")
	_, err := b.Instantiate(ctx)
	return err
}

func (r *WASMRuntime) instance(m api.Module) *instanceRuntime {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instances[m.Name()]
}
func (r *WASMRuntime) writeSecret(m api.Module, name string, p, max uint32) int32 {
	i := r.instance(m)
	if i == nil || !i.manifest.GrantedCapability(CapabilitySecrets) {
		return 0
	}
	v := i.secrets[name]
	if len(v) == 0 || uint32(len(v)) > max || !m.Memory().Write(p, v) {
		return 0
	}
	return int32(len(v))
}

func (r *WASMRuntime) Load(ctx context.Context, manifest Manifest, dataRoot string) error {
	if err := manifest.Validate(); err != nil {
		return err
	}
	artifact := manifest.Artifact.Path
	if !filepath.IsAbs(artifact) {
		artifact = filepath.Join(dataRoot, artifact)
	}
	wasm, err := os.ReadFile(artifact)
	if err != nil {
		return err
	}
	if !verifySHA256(wasm, manifest.Artifact.SHA256) {
		return fmt.Errorf("%w: artifact integrity", ErrInvalidManifest)
	}
	secrets := map[string][]byte{}
	if manifest.GrantedCapability(CapabilitySecrets) {
		for name, path := range manifest.Secrets {
			if !filepath.IsAbs(path) {
				path = filepath.Join(r.secretsRoot, path)
			}
			v, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			secrets[name] = []byte(strings.TrimSpace(string(v)))
		}
	}
	compiled, err := r.runtime.CompileModule(ctx, wasm)
	if err != nil {
		return err
	}
	ictx, cancel := context.WithCancel(context.Background())
	inst := &instanceRuntime{manifest: manifest, ctx: ictx, cancel: cancel, secrets: secrets}
	r.mu.Lock()
	if r.closed || r.instances[manifest.InstanceID] != nil {
		r.mu.Unlock()
		cancel()
		return ErrInvalidManifest
	}
	r.instances[manifest.InstanceID] = inst
	r.mu.Unlock()
	mod, err := r.runtime.InstantiateModule(ictx, compiled, wazero.NewModuleConfig().WithName(manifest.InstanceID).WithStartFunctions())
	if err != nil {
		r.remove(manifest.InstanceID)
		cancel()
		return err
	}
	inst.module = mod
	buf, size := mod.ExportedFunction("gateway_event_buf"), mod.ExportedFunction("gateway_event_buf_size")
	abi, init := mod.ExportedFunction("gateway_abi_version"), mod.ExportedFunction("gateway_init")
	if buf == nil || size == nil || abi == nil || init == nil {
		r.Unload(context.Background(), manifest.InstanceID)
		return ErrUnsupportedABI
	}
	av, _ := abi.Call(ictx)
	if len(av) == 0 || int32(av[0]) != GuestABIVersion {
		_ = r.Unload(context.Background(), manifest.InstanceID)
		return ErrUnsupportedABI
	}
	bp, e := buf.Call(ictx)
	bs, se := size.Call(ictx)
	if e != nil || se != nil || len(bp) == 0 || len(bs) == 0 || bs[0] < maxGuestEvent {
		_ = r.Unload(context.Background(), manifest.InstanceID)
		return ErrUnsupportedABI
	}
	inst.eventPtr = uint32(bp[0])
	_, err = init.Call(ictx)
	if err != nil {
		_ = r.Unload(context.Background(), manifest.InstanceID)
	}
	return err
}
func verifySHA256(data []byte, want string) bool {
	return fmt.Sprintf("%x", sha256Sum(data)) == strings.ToLower(want)
}
func sha256Sum(data []byte) [32]byte    { return sha256.Sum256(data) }
func (r *WASMRuntime) remove(id string) { r.mu.Lock(); delete(r.instances, id); r.mu.Unlock() }
func (r *WASMRuntime) Poll(id string) error {
	i := r.get(id)
	if i == nil {
		return ErrInvalidManifest
	}
	i.callMu.Lock()
	defer i.callMu.Unlock()
	fn := i.module.ExportedFunction("gateway_poll")
	if fn == nil {
		return ErrUnsupportedABI
	}
	_, err := fn.Call(i.ctx)
	return err
}
func (r *WASMRuntime) HandleEvent(id, typ string, payload []byte) error {
	i := r.get(id)
	if i == nil {
		return ErrInvalidManifest
	}
	event, _ := json.Marshal(GuestMessage{Version: 0, Type: typ, Payload: payload})
	if len(event) > maxGuestEvent {
		return ErrInvalidManifest
	}
	i.callMu.Lock()
	defer i.callMu.Unlock()
	if !i.module.Memory().Write(i.eventPtr, event) {
		return ErrInvalidManifest
	}
	fn := i.module.ExportedFunction("gateway_handle_event")
	if fn == nil {
		return ErrUnsupportedABI
	}
	_, err := fn.Call(i.ctx, uint64(i.eventPtr), uint64(len(event)))
	return err
}
func (r *WASMRuntime) TakeOutbound(id string) []GuestMessage {
	i := r.get(id)
	if i == nil {
		return nil
	}
	i.outboundMu.Lock()
	defer i.outboundMu.Unlock()
	out := append([]GuestMessage(nil), i.outbound...)
	i.outbound = nil
	return out
}
func (r *WASMRuntime) get(id string) *instanceRuntime {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.instances[id]
}
func (r *WASMRuntime) Unload(ctx context.Context, id string) error {
	i := r.get(id)
	if i == nil {
		return nil
	}
	r.remove(id)
	if fn := i.module.ExportedFunction("gateway_shutdown"); fn != nil {
		_, _ = fn.Call(i.ctx)
	}
	i.cancel()
	return i.module.Close(ctx)
}
func (r *WASMRuntime) Close(ctx context.Context) error {
	r.mu.Lock()
	ids := make([]string, 0, len(r.instances))
	for id := range r.instances {
		ids = append(ids, id)
	}
	r.closed = true
	r.mu.Unlock()
	for _, id := range ids {
		_ = r.Unload(ctx, id)
	}
	return r.runtime.Close(ctx)
}

func (r *WASMRuntime) httpGet(ctx context.Context, m api.Module, up, un, bp, bmax, rp uint32) {
	result := func(n, code, status uint32) {
		var b [12]byte
		binary.LittleEndian.PutUint32(b[:4], n)
		binary.LittleEndian.PutUint32(b[4:8], code)
		binary.LittleEndian.PutUint32(b[8:], status)
		m.Memory().Write(rp, b[:])
	}
	i := r.instance(m)
	if i == nil || !i.manifest.GrantedCapability(CapabilityHTTP) {
		result(0, 2, 0)
		return
	}
	if bmax > maxHTTPResponse {
		bmax = maxHTTPResponse
	}
	raw, ok := m.Memory().Read(up, un)
	if !ok {
		result(0, 1, 0)
		return
	}
	u, err := url.Parse(string(raw))
	if err != nil || u.Scheme != "https" || !allowedHost(u.Hostname(), i.manifest.HTTPAllowlist) {
		result(0, 2, 0)
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(cctx, http.MethodGet, u.String(), nil)
	client := &http.Client{CheckRedirect: func(req *http.Request, _ []*http.Request) error {
		if req.URL.Scheme != "https" || !allowedHost(req.URL.Hostname(), i.manifest.HTTPAllowlist) {
			return ErrCapabilityDenied
		}
		return nil
	}}
	resp, err := client.Do(req)
	if err != nil {
		result(0, 4, 0)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		result(0, 6, uint32(resp.StatusCode))
		return
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, int64(bmax)+1))
	if err != nil || len(body) > int(bmax) || !m.Memory().Write(bp, body) {
		result(0, 5, uint32(resp.StatusCode))
		return
	}
	result(uint32(len(body)), 0, uint32(resp.StatusCode))
}
func allowedHost(host string, allow []string) bool {
	for _, v := range allow {
		if strings.EqualFold(host, v) {
			return true
		}
	}
	return false
}
func (r *WASMRuntime) stateGet(ctx context.Context, m api.Module, kp, kn, vp, vmax uint32) int32 {
	i := r.instance(m)
	if i == nil || !i.manifest.GrantedCapability(CapabilityState) {
		return -1
	}
	key, ok := m.Memory().Read(kp, kn)
	if !ok {
		return -1
	}
	v, err := r.state.Get(ctx, i.manifest.InstanceID, string(key))
	if err != nil || uint32(len(v)) > vmax || !m.Memory().Write(vp, v) {
		return -1
	}
	return int32(len(v))
}
func (r *WASMRuntime) statePut(ctx context.Context, m api.Module, kp, kn, vp, vn uint32) int32 {
	i := r.instance(m)
	if i == nil || !i.manifest.GrantedCapability(CapabilityState) {
		return -1
	}
	key, ok := m.Memory().Read(kp, kn)
	if !ok {
		return -1
	}
	v, ok := m.Memory().Read(vp, vn)
	if !ok {
		return -1
	}
	if err := r.state.Put(ctx, i.manifest.InstanceID, string(key), v); err != nil {
		return -1
	}
	return 0
}
