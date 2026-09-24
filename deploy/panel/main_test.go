package main

import (
	"strings"
	"testing"
)

func TestValidateConfig(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		ok   bool
	}{
		{"disabled defaults", defaults(), true},
		{"yandex needs url", Config{Enabled: true, Transport: "yandex", Mode: "l3", Codec: "batched"}, false},
		{"yandex url", Config{Enabled: true, Transport: "yandex", URL: "https://docs.yandex.ru/docs/view", Mode: "l3", Codec: "batched"}, true},
		{"cups no url", Config{Enabled: true, Transport: "cupsonline", Mode: "l4", Codec: "legacy"}, true},
		{"bad scheme", Config{Enabled: true, Transport: "mailru", URL: "file:///etc/passwd", Mode: "l3", Codec: "batched"}, false},
		{"unknown transport", Config{Transport: "other", Mode: "l3", Codec: "batched"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateConfig(tc.cfg)
			if (err == nil) != tc.ok {
				t.Fatalf("validateConfig() error = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestValidateNode(t *testing.T) {
	n := Node{Name: "edge-1", BaseURL: "https://node.example:8088", Token: strings.Repeat("a", 32), TLSSHA256: strings.Repeat("ab", 32)}
	if err := validateNode(n); err != nil {
		t.Fatal(err)
	}
	n.BaseURL = "http://node.example:8088"
	if err := validateNode(n); err == nil {
		t.Fatal("expected HTTPS validation error")
	}
}
