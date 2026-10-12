package egress

import (
	"context"
	"errors"
	"testing"
)

func TestWarpCreatePersistsIntentAndDoesNotRepeatUncertainRegistration(t *testing.T) {
	dir := t.TempDir()
	m, err := NewWarpManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.run = func(context.Context, string, []string, string) error {
		calls++
		return errors.New("simulated registration timeout")
	}
	p, err := m.Create(context.Background(), "warp-1", "test", true)
	if err == nil || p.Phase != "failed" || !p.RegistrationAttempted {
		t.Fatalf("unexpected create result: %#v %v", p, err)
	}
	if _, err = m.Resume(context.Background(), "warp-1"); err == nil {
		t.Fatal("uncertain registration must not be retried")
	}
	if calls != 1 {
		t.Fatalf("registration command repeated: %d", calls)
	}
	stored, err := m.Get("warp-1")
	if err != nil {
		t.Fatal(err)
	}
	if !stored.RegistrationAttempted || stored.Registered {
		t.Fatalf("unexpected persisted state: %#v", stored)
	}
}

func TestWarpRejectsInvalidIDsAndTermsBeforeCreatingFiles(t *testing.T) {
	m, err := NewWarpManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Create(context.Background(), "../escape", "bad", true); err == nil {
		t.Fatal("expected id rejection")
	}
	if _, err = m.Create(context.Background(), "warp-2", "bad", false); err == nil {
		t.Fatal("expected terms rejection")
	}
}
