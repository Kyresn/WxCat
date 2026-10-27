package idmap

import (
	"path/filepath"
	"testing"
)

func TestNumIsStableAndPersistent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "idmap.json")

	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	a := s.Num(KindUser, "wxid_abc")
	b := s.Num(KindUser, "wxid_abc")
	if a != b || a == 0 {
		t.Fatalf("unstable id: %d vs %d", a, b)
	}
	group := s.Num(KindGroup, "123@chatroom")
	if group == a {
		t.Fatal("user and group ids must not collide")
	}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reopened.Num(KindUser, "wxid_abc"); got != a {
		t.Fatalf("id changed after reopen: %d vs %d", got, a)
	}
	wxid, kind, ok := reopened.Lookup(group)
	if !ok || wxid != "123@chatroom" || kind != KindGroup {
		t.Fatalf("lookup failed: %q %q %v", wxid, kind, ok)
	}
}

func TestCollisionResolution(t *testing.T) {
	s, err := Open(filepath.Join(t.TempDir(), "idmap.json"))
	if err != nil {
		t.Fatal(err)
	}

	first := s.Num(KindUser, "wxid_one")
	// Force a collision by claiming the derived id of another wxid.
	other := derive(KindUser, "wxid_two")
	if other == first {
		t.Skip("hash collision in fixture")
	}
	s.mu.Lock()
	s.byID[first] = entry{ID: first, Wxid: "wxid_one", Kind: KindUser}
	s.byKey[key(KindUser, "wxid_one")] = first
	s.byID[other] = entry{ID: other, Wxid: "wxid_other", Kind: KindUser}
	s.mu.Unlock()

	got := s.Num(KindUser, "wxid_two")
	if got == other {
		t.Fatal("expected the colliding id to be bumped")
	}
	if _, exists := s.byID[got]; !exists {
		t.Fatal("allocated id missing from store")
	}
}
