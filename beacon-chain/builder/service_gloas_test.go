package builder

import (
	"context"
	"errors"
	"sync"
	"testing"

	builderapi "github.com/OffchainLabs/prysm/v7/api/client/builder"
	buildertesting "github.com/OffchainLabs/prysm/v7/api/client/builder/testing"
	"github.com/OffchainLabs/prysm/v7/consensus-types/primitives"
	eth "github.com/OffchainLabs/prysm/v7/proto/prysm/v1alpha1"
	"github.com/OffchainLabs/prysm/v7/testing/require"
)

// fakeBuilderClient is a per-URL builder client for exercising the multiplex
// fan-out: it returns a configurable bid/error and records calls.
type fakeBuilderClient struct {
	buildertesting.MockClient
	url       string
	bid       *eth.SignedExecutionPayloadBid
	getErr    error
	prefCount int
	mu        sync.Mutex
	gotAuths  []*eth.SignedRequestAuthV1
}

func (f *fakeBuilderClient) NodeURL() string { return f.url }

func (f *fakeBuilderClient) GetExecutionPayloadBid(_ context.Context, _ primitives.Slot, _ [32]byte, _ [32]byte, _ [48]byte, auth *eth.SignedRequestAuthV1) (*eth.SignedExecutionPayloadBid, error) {
	f.mu.Lock()
	f.gotAuths = append(f.gotAuths, auth)
	f.mu.Unlock()
	return f.bid, f.getErr
}

func (f *fakeBuilderClient) SubmitBuilderPreferences(context.Context, [48]byte, *eth.BuilderPreferencesRequestV1) error {
	f.prefCount++
	return nil
}

func authFor(url string) *eth.SignedRequestAuthV1 {
	return &eth.SignedRequestAuthV1{Message: &eth.RequestAuthV1{Data: []byte(url)}}
}

func bidWithValue(v primitives.Gwei) *eth.SignedExecutionPayloadBid {
	return &eth.SignedExecutionPayloadBid{Message: &eth.ExecutionPayloadBid{Value: v}}
}

func newMultiplexService(t *testing.T, clients map[string]*fakeBuilderClient) *Service {
	s, err := NewService(t.Context())
	require.NoError(t, err)
	s.dial = func(url string) (builderapi.BuilderClient, error) {
		c, ok := clients[url]
		if !ok {
			return nil, errors.New("no client for " + url)
		}
		return c, nil
	}
	return s
}

func TestGetExecutionPayloadBid_FanOutAndDedup(t *testing.T) {
	clients := map[string]*fakeBuilderClient{
		"http://a": {url: "http://a", bid: bidWithValue(100)},
		"http://b": {url: "http://b", bid: bidWithValue(200)},
	}
	s := newMultiplexService(t, clients)

	auths := []*eth.SignedRequestAuthV1{authFor("http://a"), authFor("http://b"), authFor("http://a")}
	bids, err := s.GetExecutionPayloadBid(t.Context(), 1, [32]byte{}, [32]byte{}, [48]byte{}, auths, "")
	require.NoError(t, err)
	require.Equal(t, 2, len(bids))

	got := map[string]primitives.Gwei{}
	for _, pb := range bids {
		got[pb.BuilderURL] = pb.Bid.Message.Value
	}
	require.Equal(t, primitives.Gwei(100), got["http://a"])
	require.Equal(t, primitives.Gwei(200), got["http://b"])
}

func TestGetExecutionPayloadBid_SkipsErrorsAndNil(t *testing.T) {
	clients := map[string]*fakeBuilderClient{
		"http://ok":   {url: "http://ok", bid: bidWithValue(50)},
		"http://err":  {url: "http://err", getErr: errors.New("boom")},
		"http://none": {url: "http://none", bid: nil},
	}
	s := newMultiplexService(t, clients)

	// http://nodial has no client; dialing it fails and is skipped.
	auths := []*eth.SignedRequestAuthV1{authFor("http://ok"), authFor("http://err"), authFor("http://none"), authFor("http://nodial")}
	bids, err := s.GetExecutionPayloadBid(t.Context(), 1, [32]byte{}, [32]byte{}, [48]byte{}, auths, "")
	require.NoError(t, err)
	require.Equal(t, 1, len(bids))
	require.Equal(t, "http://ok", bids[0].BuilderURL)
}

func TestGetExecutionPayloadBid_NoAuths(t *testing.T) {
	s := newMultiplexService(t, nil)
	bids, err := s.GetExecutionPayloadBid(t.Context(), 1, [32]byte{}, [32]byte{}, [48]byte{}, nil, "")
	require.NoError(t, err)
	require.Equal(t, 0, len(bids))
}

func TestClientFor_SeedsFlagClientAndCachesDials(t *testing.T) {
	seed := &fakeBuilderClient{url: "http://seed"}
	s, err := NewService(t.Context(), WithBuilderClient(seed))
	require.NoError(t, err)

	dialed := 0
	s.dial = func(url string) (builderapi.BuilderClient, error) {
		dialed++
		return &fakeBuilderClient{url: url}, nil
	}

	// The flag client seeds the map, so its URL is served without dialing.
	c, err := s.clientFor("http://seed")
	require.NoError(t, err)
	require.Equal(t, "http://seed", c.NodeURL())
	require.Equal(t, 0, dialed)

	// A new URL dials once and is then cached.
	_, err = s.clientFor("http://new")
	require.NoError(t, err)
	_, err = s.clientFor("http://new")
	require.NoError(t, err)
	require.Equal(t, 1, dialed)
}

func TestSubmitBuilderPreferences_DialsPerURL(t *testing.T) {
	fc := &fakeBuilderClient{url: "http://b"}
	s := newMultiplexService(t, map[string]*fakeBuilderClient{"http://b": fc})

	req := &eth.BuilderPreferencesRequestV1{
		Preferences: &eth.BuilderPreferencesV1{},
		Auth:        authFor("http://b"),
	}
	require.NoError(t, s.SubmitBuilderPreferences(t.Context(), [48]byte{}, req, ""))
	require.Equal(t, 1, fc.prefCount)

	err := s.SubmitBuilderPreferences(t.Context(), [48]byte{}, &eth.BuilderPreferencesRequestV1{Auth: authFor("")}, "")
	require.ErrorContains(t, "missing builder url", err)
}

func TestGetExecutionPayloadBid_ProxyOverridesDial(t *testing.T) {
	proxy := &fakeBuilderClient{url: "http://proxy", bid: bidWithValue(10)}
	s := newMultiplexService(t, map[string]*fakeBuilderClient{"http://proxy": proxy})

	auths := []*eth.SignedRequestAuthV1{authFor("http://a"), authFor("http://b")}
	bids, err := s.GetExecutionPayloadBid(t.Context(), 1, [32]byte{}, [32]byte{}, [48]byte{}, auths, "http://proxy")
	require.NoError(t, err)
	require.Equal(t, 2, len(bids))

	// Bids keep the builder identity, not the proxy.
	got := map[string]bool{}
	for _, pb := range bids {
		got[pb.BuilderURL] = true
	}
	require.Equal(t, true, got["http://a"])
	require.Equal(t, true, got["http://b"])

	// Both requests hit the proxy carrying the auths signed over the builder URLs.
	require.Equal(t, 2, len(proxy.gotAuths))
	forwarded := map[string]bool{}
	for _, a := range proxy.gotAuths {
		forwarded[string(a.GetMessage().GetData())] = true
	}
	require.Equal(t, true, forwarded["http://a"])
	require.Equal(t, true, forwarded["http://b"])
}

func TestSubmitBuilderPreferences_ProxyOverridesDial(t *testing.T) {
	proxy := &fakeBuilderClient{url: "http://proxy"}
	s := newMultiplexService(t, map[string]*fakeBuilderClient{"http://proxy": proxy})

	req := &eth.BuilderPreferencesRequestV1{
		Preferences: &eth.BuilderPreferencesV1{},
		Auth:        authFor("http://b"),
	}
	require.NoError(t, s.SubmitBuilderPreferences(t.Context(), [48]byte{}, req, "http://proxy"))
	require.Equal(t, 1, proxy.prefCount)

	// The auth is still required even when a proxy is given.
	err := s.SubmitBuilderPreferences(t.Context(), [48]byte{}, &eth.BuilderPreferencesRequestV1{Auth: authFor("")}, "http://proxy")
	require.ErrorContains(t, "missing builder url", err)
}
