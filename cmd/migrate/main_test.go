package main

import "testing"

func TestSafeSourceIDRejectsCredentialsAndQuery(t *testing.T) {
	if got, err := safeSourceID("https://ha.example.test/base"); err != nil ||
		got != "https://ha.example.test/base" {
		t.Fatalf("safeSourceID() = %q, %v", got, err)
	}
	for _, unsafe := range []string{
		"https://token@ha.example.test", "https://ha.example.test/?token=secret", "not-a-url",
		"https://ha.example.test/#secret", "ftp://ha.example.test",
	} {
		if _, err := safeSourceID(unsafe); err == nil {
			t.Fatalf("unsafe source URL accepted: %s", unsafe)
		}
	}
}

func TestImportCommandAcceptsSourceTokenReference(t *testing.T) {
	cmd := newImportCommand()
	if cmd.Flags().Lookup("source-token-secret") == nil || cmd.Flags().Lookup("source-token-key") == nil {
		t.Fatal("source token Secret reference flags are missing")
	}
	if got := cmd.Flags().Lookup("source-token-key").DefValue; got != "token" {
		t.Fatalf("default token key = %q", got)
	}
}

func TestImportCommandAcceptsRecoveryKeyReference(t *testing.T) {
	cmd := newImportCommand()
	if cmd.Flags().Lookup("recovery-key-secret") == nil || cmd.Flags().Lookup("recovery-key-key") == nil {
		t.Fatal("recovery key Secret reference flags are missing")
	}
	if got := cmd.Flags().Lookup("recovery-key-key").DefValue; got != "key" {
		t.Fatalf("default recovery key = %q", got)
	}
}
