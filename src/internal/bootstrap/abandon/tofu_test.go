package abandon_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"strange.company/sc/internal/bootstrap/install"
)

// fakeTofu is the OpenTofu seam of the fake install. It plays the storage root module: ApplyStorage creates the
// buckets in the S3 world with the credential the spec names (the second account for StateAccount buckets) and
// writes the encrypted local state; Migrate copies that state to the remote backend object; StateList reads
// whichever of the two the caller names. Nothing here decides which bucket goes into which account: that is the
// orchestrator's job, and the spec it passes is what the tests inspect.
type fakeTofu struct {
	w *s3World

	mu       sync.Mutex
	plans    int
	applies  int
	migrates int
	specs    []install.StorageSpec
	// dropRemoteAddress makes the remote state list lose one address (a damaged migration).
	dropRemoteAddress bool
}

var _ install.Tofu = (*fakeTofu)(nil)

func addrOf(bucket string) []string {
	id := strings.ReplaceAll(bucket, "-", "_")
	return []string{"aws_s3_bucket." + id, "aws_s3_bucket_versioning." + id}
}

func (f *fakeTofu) counts() (plans, applies, migrates int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.plans, f.applies, f.migrates
}

func (f *fakeTofu) PlanStorage(_ context.Context, s install.StorageSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.plans++
	f.specs = append(f.specs, s)
	return fmt.Sprintf("fake tofu plan: %d buckets to add", len(s.Buckets)), nil
}

func (f *fakeTofu) ApplyStorage(_ context.Context, s install.StorageSpec) error {
	f.mu.Lock()
	f.applies++
	f.specs = append(f.specs, s)
	f.mu.Unlock()
	var addrs []string
	for _, b := range s.Buckets {
		key := s.Main.AccessKey
		if b.StateAccount {
			if s.State == nil {
				return errors.New("fake tofu: a state-account bucket without a state account")
			}
			key = s.State.AccessKey
		}
		if err := f.w.createBucket(key, b.Name); err != nil {
			return fmt.Errorf("fake tofu: creating %s: %w", b.Name, err)
		}
		addrs = append(addrs, addrOf(b.Name)...)
	}
	sort.Strings(addrs)
	body, _ := json.Marshal(map[string]any{"version": 4, "resources": addrs})
	if err := os.MkdirAll(filepath.Dir(s.LocalState), 0o700); err != nil {
		return err
	}
	// Real tofu leaves the state and a backup next to each other; the plan deletes storage.local.tfstate*.
	if err := os.WriteFile(s.LocalState, body, 0o600); err != nil {
		return err
	}
	return os.WriteFile(s.LocalState+".backup", body, 0o600)
}

func (f *fakeTofu) localAddrs(s install.StorageSpec) ([]string, error) {
	b, err := os.ReadFile(s.LocalState)
	if err != nil {
		return nil, fmt.Errorf("fake tofu: no local state: %w", err)
	}
	var doc struct {
		Resources []string `json:"resources"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	return doc.Resources, nil
}

func (f *fakeTofu) StateList(_ context.Context, s install.StorageSpec, remote *install.RemoteState) ([]string, error) {
	if remote == nil {
		return f.localAddrs(s)
	}
	data, ok := f.w.read(remote.Bucket, remote.Key)
	if !ok {
		return nil, fmt.Errorf("fake tofu: no remote state at %s/%s", remote.Bucket, remote.Key)
	}
	var doc struct {
		Resources []string `json:"resources"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	out := doc.Resources
	f.mu.Lock()
	drop := f.dropRemoteAddress
	f.mu.Unlock()
	if drop && len(out) > 0 {
		out = out[1:]
	}
	return out, nil
}

func (f *fakeTofu) Migrate(_ context.Context, s install.StorageSpec, remote install.RemoteState) error {
	f.mu.Lock()
	f.migrates++
	f.mu.Unlock()
	b, err := os.ReadFile(s.LocalState)
	if err != nil {
		return fmt.Errorf("fake tofu: no local state to migrate: %w", err)
	}
	return f.w.write(remote.S3.AccessKey, remote.Bucket, remote.Key, b)
}

func (f *fakeTofu) Changes(context.Context, install.StorageSpec, *install.RemoteState) (int, error) {
	return 0, nil
}
