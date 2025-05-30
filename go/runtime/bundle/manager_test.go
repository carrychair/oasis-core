package bundle

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/oasisprotocol/oasis-core/go/common/crypto/hash"
	"github.com/oasisprotocol/oasis-core/go/runtime/volume"
)

type mockStore struct {
	manifestHashes map[hash.Hash]struct{}
}

func newMockStore() *mockStore {
	return &mockStore{
		manifestHashes: make(map[hash.Hash]struct{}),
	}
}

func (r *mockStore) HasManifest(hash hash.Hash) bool {
	_, ok := r.manifestHashes[hash]
	return ok
}

func (r *mockStore) AddManifest(manifest *ExplodedManifest) error {
	r.manifestHashes[manifest.Hash()] = struct{}{}
	return nil
}

func (r *mockStore) Manifests() []*ExplodedManifest {
	panic("not implemented")
}

func (r *mockStore) ManifestsWithLabels(map[string]string) []*ExplodedManifest {
	panic("not implemented")
}

func (r *mockStore) RemoveManifest(hash.Hash) bool {
	panic("not implemented")
}

func (r *mockStore) RemoveManifestsWithLabels(map[string]string) int {
	panic("not implemented")
}

type mockVolumeManager struct{}

func newMockVolumeManager() *mockVolumeManager {
	return &mockVolumeManager{}
}

func (m *mockVolumeManager) GetOrCreate(map[string]string) (*volume.Volume, error) {
	panic("not implemented")
}

func TestRegisterManifest(t *testing.T) {
	store := newMockStore()
	volumeManager := newMockVolumeManager()
	manager, err := NewManager("", nil, store, volumeManager)
	require.NoError(t, err)

	manifests := []*ExplodedManifest{
		{
			Manifest:        &Manifest{Name: "manifest1"},
			ExplodedDataDir: "dir1",
		},
		{
			Manifest:        &Manifest{Name: "manifest2"},
			ExplodedDataDir: "dir2",
		},
	}

	err = manager.registerManifests(manifests)
	require.NoError(t, err)
	require.Equal(t, len(manifests), len(store.manifestHashes))
}
