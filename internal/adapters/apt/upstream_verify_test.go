package apt

import (
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/clearsign"
	"github.com/ProtonMail/go-crypto/openpgp/packet"

	"github.com/ynw0/airgap-mirror/internal/domain"
)

func signedReleaseFixture(t *testing.T, body []byte) (string, []byte, []byte) {
	t.Helper()
	gpg, err := exec.LookPath("gpg")
	if err != nil {
		t.Skip("GnuPG is required for real signature fixtures")
	}
	dir, err := os.MkdirTemp("", "agp-gpg-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if gpgconf, e := exec.LookPath("gpgconf"); e == nil {
			_ = exec.Command(gpgconf, "--homedir", dir, "--kill", "gpg-agent").Run()
		}
		_ = os.RemoveAll(dir)
	})
	run := func(args ...string) []byte {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, gpg, append([]string{"--no-options", "--homedir", dir, "--batch", "--no-tty", "--pinentry-mode", "loopback", "--passphrase", ""}, args...)...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("gpg fixture: %v: %s", err, stderr.String())
		}
		return out
	}
	run("--quick-gen-key", "Airgap fixture <fixture@example.invalid>", "ed25519", "sign", "1d")
	keys := run("--export")
	release := filepath.Join(dir, "Release")
	if err = os.WriteFile(release, body, 0600); err != nil {
		t.Fatal(err)
	}
	sig := run("--output", "-", "--detach-sign", release)
	inline := run("--output", "-", "--clearsign", release)
	return base64.StdEncoding.EncodeToString(keys), sig, inline
}

func TestRealUpstreamSignaturesRejectTamperingAndWrongKeys(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	body := []byte(fmt.Sprintf("Suite: stable\nCodename: bookworm\nDate: %s\nValid-Until: %s\nSHA256:\n %s 1 main/Packages\n", now.Format(time.RFC1123), now.Add(time.Hour).Format(time.RFC1123), strings.Repeat("a", 64)))
	encoded, sig, inline := signedReleaseFixture(t, body)
	keys, err := decodeTrustedKeyring(encoded)
	if err != nil {
		t.Fatal(err)
	}
	otherEncoded, _, _ := signedReleaseFixture(t, body)
	other, err := decodeTrustedKeyring(otherEncoded)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name               string
		keys, release, sig []byte
		inline, ok         bool
	}{
		{"detached", keys, body, sig, false, true},
		{"clear signed", keys, inline, nil, true, true},
		{"detached tamper", keys, bytes.Replace(body, []byte("bookworm"), []byte("bullseye"), 1), sig, false, false},
		{"inline tamper", keys, bytes.Replace(inline, []byte("bookworm"), []byte("bullseye"), 1), nil, true, false},
		{"unknown key", other, body, sig, false, false},
		{"unsigned suffix", keys, append(append([]byte(nil), inline...), []byte("Suite: other\n")...), nil, true, false},
		{"invalid detached signature", keys, body, []byte("fake"), false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain, err := verifyUpstreamRelease(ctx, tc.keys, tc.release, tc.sig, tc.inline)
			if !tc.ok {
				if err == nil {
					t.Fatal("invalid signature accepted")
				}
				return
			}
			if err != nil || !bytes.Equal(plain, body) {
				t.Fatalf("signed content not verified: %v", err)
			}
			if err = validateReleaseFreshness(plain, Config{Suite: "bookworm", RequireValidUntil: true}, now); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestReleaseFreshness(t *testing.T) {
	now := time.Now().UTC()
	date := now.Add(-time.Hour).Format(time.RFC1123)
	for _, tc := range []struct {
		name, body string
		strict, ok bool
	}{
		{"stable archive", "Suite: stable\nDate: " + date + "\n", false, true},
		{"strict requires expiry", "Suite: stable\nDate: " + date + "\n", true, false},
		{"expired", "Suite: stable\nDate: " + date + "\nValid-Until: " + now.Add(-time.Minute).Format(time.RFC1123) + "\n", false, false},
		{"wrong suite", "Suite: unstable\nDate: " + date + "\n", false, false},
		{"future", "Suite: stable\nDate: " + now.Add(time.Hour).Format(time.RFC1123) + "\n", false, false},
		{"duplicate", "Suite: stable\nDate: " + date + "\nDate: " + date + "\n", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateReleaseFreshness([]byte(tc.body), Config{Suite: "stable", RequireValidUntil: tc.strict}, now)
			if (err == nil) != tc.ok {
				t.Fatalf("freshness: %v", err)
			}
		})
	}
}

func TestAPTProbeWithoutKeyringFailsBeforeNetwork(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("APT contacted upstream without a configured keyring")
		w.WriteHeader(500)
	}))
	defer server.Close()
	src := domain.Source{Type: domain.SourceAPT, Provider: Provider, UpstreamURL: server.URL, PublicURL: "https://repo.example/apt", ConfigJSON: json.RawMessage(`{"suite":"custom-archive","releaseMode":"inrelease"}`)}
	if _, err := New(server.Client()).Probe(context.Background(), src); !errors.Is(err, domain.ErrInvalid) {
		t.Fatalf("unconfigured trust: %v", err)
	}
	for _, invalid := range []string{
		"not-base64!",
		base64.StdEncoding.EncodeToString([]byte{0xc5, 1, 0}),               // secret-key packet
		base64.StdEncoding.EncodeToString([]byte{0xc6, 1, 0, 0xc7, 1, 0}),   // secret subkey
		base64.StdEncoding.EncodeToString([]byte{0xc6, 255, 0, 0, 0, 9, 1}), // truncated public key
	} {
		if _, err := decodeTrustedKeyring(invalid); !errors.Is(err, domain.ErrInvalid) {
			t.Fatalf("invalid keyring accepted: %v", err)
		}
	}
}

func TestBundledArchiveKeys(t *testing.T) {
	for _, suite := range []string{"focal", "jammy-updates", "noble-security", "bookworm", "trixie-security", "stable"} {
		keys, err := archiveKeyring(Config{Suite: suite})
		if err != nil {
			t.Fatalf("%s: %v", suite, err)
		}
		if entities, err := openpgp.ReadKeyRing(bytes.NewReader(keys)); err != nil || len(entities) == 0 {
			t.Fatalf("%s: archive keys cannot be read: %v", suite, err)
		}
	}
}

func TestVerifyReleaseWithoutGnuPG(t *testing.T) {
	config := &packet.Config{RSABits: 2048, DefaultHash: crypto.SHA256}
	entity, err := openpgp.NewEntity("Airgap test", "", "test@example.invalid", config)
	if err != nil {
		t.Fatal(err)
	}
	var keys, signature, inline bytes.Buffer
	if err = entity.Serialize(&keys); err != nil {
		t.Fatal(err)
	}
	body := []byte("Suite: stable\nSHA256:\n aaaa 1 main/Packages\n")
	if err = openpgp.DetachSign(&signature, entity, bytes.NewReader(body), config); err != nil {
		t.Fatal(err)
	}
	writer, err := clearsign.Encode(&inline, entity.PrivateKey, config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		release, signature []byte
		inline             bool
	}{
		{body, signature.Bytes(), false},
		{inline.Bytes(), nil, true},
	} {
		plain, err := verifyUpstreamRelease(context.Background(), keys.Bytes(), test.release, test.signature, test.inline)
		if err != nil || !bytes.Equal(plain, body) {
			t.Fatalf("verification without GnuPG failed: %v", err)
		}
	}
	tampered := bytes.Replace(body, []byte("stable"), []byte("testing"), 1)
	if _, err = verifyUpstreamRelease(context.Background(), keys.Bytes(), tampered, signature.Bytes(), false); err == nil {
		t.Fatal("tampered release accepted")
	}
	double := append(append([]byte(nil), signature.Bytes()...), signature.Bytes()...)
	if _, err = verifyUpstreamRelease(context.Background(), keys.Bytes(), body, double, false); err != nil {
		t.Fatalf("multiple valid signatures: %v", err)
	}
	var invalid bytes.Buffer
	if err = openpgp.DetachSign(&invalid, entity, bytes.NewReader(tampered), config); err != nil {
		t.Fatal(err)
	}
	mixed := append(append([]byte(nil), signature.Bytes()...), invalid.Bytes()...)
	if _, err = verifyUpstreamRelease(context.Background(), keys.Bytes(), body, mixed, false); err == nil {
		t.Fatal("accepted a valid signature followed by a bad signature")
	}
}
