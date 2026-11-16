package recoverykit

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"filippo.io/age"
	"filippo.io/age/armor"
)

func testKit() Kit {
	return Kit{Generation: 2, CreatedAt: "2026-11-17T10:00:00Z", Reason: "repository main created", Hostname: "gaia1",
		EvidenceKeyID: "abc", EvidenceKey: "-----BEGIN PRIVATE KEY-----\nx\n-----END PRIVATE KEY-----\n", EvidencePub: "pub",
		SSHKey: "ssh", SSHPub: "ssh-ed25519 AAAA gaia@gaia1", ServerConfig: []byte(`{"data_dir":"/var/lib/gaia"}`),
		StateRepository: "main", Repositories: []Repository{{ID: 1, Name: "main", Kind: "filesystem", Location: "/srv/gaia/main", Password: "pw"}}}
}

func TestSealOpen(t *testing.T) {
	workFactor = 10
	const pass = "correct horse battery"
	b, err := Seal(testKit(), pass)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte(armor.Header)) {
		t.Fatalf("not armoured: %q", b[:40])
	}
	k, err := Open(b, pass)
	if err != nil {
		t.Fatal(err)
	}
	if k.Format != Format || k.Instructions == "" || k.Repositories[0].Password != "pw" || string(k.ServerConfig) == "" {
		t.Fatalf("round trip: %+v", k)
	}
	if _, err := Open(b, "wrong passphrase here"); !errors.Is(err, ErrPassphrase) {
		t.Fatalf("wrong passphrase: %v", err)
	}
	// What the standalone age tool does: an scrypt identity and the armour, nothing of Gaia's.
	id, err := age.NewScryptIdentity(pass)
	if err != nil {
		t.Fatal(err)
	}
	r, err := age.Decrypt(armor.NewReader(bytes.NewReader(b)), id)
	if err != nil {
		t.Fatal(err)
	}
	var plain bytes.Buffer
	plain.ReadFrom(r)
	if !strings.Contains(plain.String(), `"format": "gaia-recovery-kit/1"`) {
		t.Fatalf("plaintext: %s", plain.String())
	}
}

func TestSealRefuses(t *testing.T) {
	workFactor = 10
	if _, err := Seal(testKit(), "short"); err == nil {
		t.Fatal("a short passphrase was accepted")
	}
	if _, err := Seal(testKit(), " leading space pass"); err == nil {
		t.Fatal("a passphrase with leading white space was accepted")
	}
	k := testKit()
	k.StateRepository = "other"
	if _, err := Seal(k, "long enough passphrase"); err == nil {
		t.Fatal("a state repository outside the kit was accepted")
	}
	k = testKit()
	k.Repositories = append(k.Repositories, k.Repositories[0])
	if _, err := Seal(k, "long enough passphrase"); err == nil {
		t.Fatal("a repository twice was accepted")
	}
	k = testKit()
	k.EvidenceKey = ""
	if _, err := Seal(k, "long enough passphrase"); err == nil {
		t.Fatal("a kit without the evidence key was accepted")
	}
}
