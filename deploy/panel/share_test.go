package main

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func decodeShareTest(t *testing.T, link string) shareConfig {
	t.Helper()
	if !strings.HasPrefix(link, "openflux://v1/") {
		t.Fatalf("unexpected share prefix: %q", link)
	}
	packed, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(link, "openflux://v1/"))
	if err != nil {
		t.Fatal(err)
	}
	r := flate.NewReader(bytes.NewReader(packed))
	defer r.Close()
	raw, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	var c shareConfig
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	return c
}

func TestShareSessionMatchesUpstreamFields(t *testing.T) {
	c := Connection{Name: "mix", Config: Config{Transport: "yandex", Codec: "batched", Transports: []TransportLink{
		{Type: "direct", Priority: 100},
		{Type: "yandex", URL: "https://disk.yandex.ru/i/document", Priority: 75},
		{Type: "mailru", URL: "https://cloud.mail.ru/public/document", Priority: 15},
	}, DirectListen: ":39000"}}
	spec, err := buildShareConfig(c, "", strings.Repeat("a", 64), "203.0.113.9")
	if err != nil {
		t.Fatal(err)
	}
	if !spec.Negotiate || spec.Context != "https://disk.yandex.ru/i/document" || spec.Transports[0].Dial != "203.0.113.9:39000" {
		t.Fatalf("incorrect share spec: %+v", spec)
	}
	result, err := encodeShare(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(result.QR, "data:image/png;base64,") {
		t.Fatal("QR missing")
	}
	got := decodeShareTest(t, result.Link)
	if got.Secret != spec.Secret || got.Context != spec.Context || len(got.Transports) != 3 {
		t.Fatalf("incorrect decoded share: %+v", got)
	}
}

func TestShareSingleAndCups(t *testing.T) {
	c := Connection{Name: "yndx", Config: Config{Transport: "yandex", URL: "https://disk.yandex.ru/i/example", Codec: "batched"}}
	spec, err := buildShareConfig(c, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if spec.Negotiate || spec.Context != c.URL || spec.Transports[0].URL != c.URL {
		t.Fatalf("incorrect legacy share: %+v", spec)
	}
	c.Transport, c.URL = "cupsonline", ""
	if _, err := buildShareConfig(c, "", "", ""); err == nil {
		t.Fatal("missing room code accepted")
	}
	spec, err = buildShareConfig(c, "room-code", "", "")
	if err != nil || spec.Transports[0].URL != "room-code" {
		t.Fatalf("Cups code missing: %+v, %v", spec, err)
	}
	if spec.Context != "cupsonline" {
		t.Fatalf("wrong Cups encryption context: %q", spec.Context)
	}
}

func TestShareRejectsExternalKeyAndInvalidDirectHost(t *testing.T) {
	c := Connection{ID: "share-test", Name: "direct", Config: Config{Transport: "direct", Codec: "batched", Negotiate: true, DirectListen: ":39000", EncryptionKeyFile: "/etc/other-secret"}}
	m := &Manager{connections: []Connection{c}}
	if _, err := m.connectionShare(c.ID, "example.com"); err == nil {
		t.Fatal("external key exposed")
	}
	if _, err := shareDirectHost("https://example.com"); err == nil {
		t.Fatal("URL accepted as a host")
	}
	dir := t.TempDir()
	t.Setenv("OPENFLUX_STATE_DIR", dir)
	c.EncryptionKeyFile = managedKeyPath(c.ID)
	if err := os.MkdirAll(dir+"/keys", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.EncryptionKeyFile, []byte(strings.Repeat("b", 64)), 0600); err != nil {
		t.Fatal(err)
	}
	m.connections[0] = c
	result, err := m.connectionShare(c.ID, "example.com")
	if err != nil {
		t.Fatal(err)
	}
	got := decodeShareTest(t, result.Link)
	if got.Transports[0].Dial != "example.com:39000" {
		t.Fatalf("wrong direct address: %+v", got.Transports[0])
	}
}
