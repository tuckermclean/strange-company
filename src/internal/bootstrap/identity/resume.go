package identity

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"

	"strange.company/sc/internal/clock"
)

// SetFromBundle rebuilds the secret set from an opened bundle: plan 50 section 14.3 says the bundle is the source of
// truth when the install resumes. Secrets the register keeps out of the bundle are not in the result; CompleteGenerated
// adds them again.
func SetFromBundle(reg *Register, uid string, o *Opened) (*Set, error) {
	if reg == nil || o == nil {
		return nil, fail("BUNDLE_CORRUPT", "no register or bundle to rebuild the secret set from")
	}
	var m manifestJSON
	if err := json.Unmarshal(o.Manifest, &m); err != nil {
		return nil, fail("BUNDLE_CORRUPT", "the bundle manifest is not JSON")
	}
	files := map[string][]byte{}
	for _, f := range o.Files {
		files[f.Path] = f.Data
	}
	if uid == "" {
		uid = m.InstallUID
	}
	set := &Set{InstallUID: uid, Register: reg, ClusterRecipient: m.AgeRecipients.Cluster, RecoveryRecipient: m.AgeRecipients.Recovery}
	for _, e := range m.Entries {
		data, ok := files[e.Path]
		if !ok {
			return nil, fail("BUNDLE_CORRUPT", "the bundle manifest lists %s but the archive lacks it", e.Path)
		}
		set.Secrets = append(set.Secrets, Secret{Class: e.Class, Instance: e.Instance, Format: e.Format, Value: data, InBundle: true, Generation: e.Generation})
	}
	return set, nil
}

// CompleteGenerated generates the secrets plan.install-identity owns that the set lacks (those the register keeps out
// of the recovery bundle). They never reached Git when the set is rebuilt from a bundle without an install commit.
func (s *Set) CompleteGenerated(cluster string, clk clock.Clock) error {
	fresh, err := Generate(s.Register, cluster, WithClock(clk))
	if err != nil {
		return err
	}
	for _, f := range fresh.Secrets {
		// A rotated OpenTofu passphrase lives in the instances current and previous (plan 06 section 20.3): the
		// class counts as present whichever instance holds it.
		if f.Class == "SC-TOFU-ENCRYPTION" && s.hasClass(f.Class) {
			continue
		}
		if s.Find(f.Class, f.Instance) == nil {
			s.Secrets = append(s.Secrets, f)
		}
	}
	return nil
}

// AddExternal adds a secret the operator supplied or the storage step minted, for a class and instance the register
// knows. A format of "" takes the bundle format of the class. A secret of the same class and instance is replaced.
func (s *Set) AddExternal(class, instance, format string, value []byte) error {
	if s.Register == nil {
		return fail("REGISTER_INVALID", "the set has no register")
	}
	var found *Class
	for i := range s.Register.Classes {
		if s.Register.Classes[i].ID == class {
			found = &s.Register.Classes[i]
		}
	}
	if found == nil {
		return fail("REGISTER_INVALID", "the register has no class %s", class)
	}
	inBundle := found.InRecoveryBundle
	if instance != "" {
		ok := false
		for _, in := range found.Instances {
			if in.Name == instance {
				ok, inBundle = true, in.InRecoveryBundle
			}
		}
		if !ok {
			return fail("REGISTER_INVALID", "the register has no instance %s of %s", instance, class)
		}
	}
	if format == "" {
		format = bundleFormat(found.Format)
	}
	sec := Secret{Class: class, Instance: instance, Format: format, Value: bytes.Clone(value), InBundle: inBundle}
	for i := range s.Secrets {
		if s.Secrets[i].Class == class && s.Secrets[i].Instance == instance {
			s.Secrets[i] = sec
			return nil
		}
	}
	s.Secrets = append(s.Secrets, sec)
	return nil
}

// Instances lists the instance names the register holds for a class, sorted.
func (r *Register) Instances(class string) []string {
	var out []string
	for _, c := range r.Classes {
		if c.ID == class {
			for _, in := range c.Instances {
				out = append(out, in.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// objectStorageKeys renders the keys of an SC-OBJECT-STORAGE value: KEY=VALUE lines, in the generic Secret key names
// of plan 14 section 7.1 (ACCESS_KEY_ID, ACCESS_SECRET_KEY, REGION, ENDPOINT).
func objectStorageKeys(v string) []kv {
	var out []kv
	for _, l := range strings.Split(v, "\n") {
		if k, val, ok := strings.Cut(strings.TrimSpace(l), "="); ok && k != "" {
			out = append(out, kv{k, val})
		}
	}
	return out
}

func (s *Set) hasClass(class string) bool {
	for _, x := range s.Secrets {
		if x.Class == class {
			return true
		}
	}
	return false
}

// NewTofuPassphrase generates an SC-TOFU-ENCRYPTION value (32 random bytes, base64url) for the rotation of plan 06
// section 20.3.
func NewTofuPassphrase() (string, error) {
	b, err := randBytes(32)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
