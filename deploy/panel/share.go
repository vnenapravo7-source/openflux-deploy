package main

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"strings"

	qrcode "github.com/skip2/go-qrcode"
)

// These fields and the wire encoding match upstream OpenFlux/share v1.
type shareTransport struct {
	Type     string `json:"type"`
	Name     string `json:"name,omitempty"`
	URL      string `json:"url,omitempty"`
	Priority int    `json:"priority,omitempty"`
	Dial     string `json:"dial,omitempty"`
}

type shareConfig struct {
	Name       string           `json:"name,omitempty"`
	Negotiate  bool             `json:"negotiate,omitempty"`
	Codec      string           `json:"codec,omitempty"`
	Secret     string           `json:"secret,omitempty"`
	Context    string           `json:"context,omitempty"`
	Transports []shareTransport `json:"transports"`
}

type shareResult struct {
	Link string `json:"link"`
	QR   string `json:"qr"`
}

// coreShareResult is the stable JSON reply from OpenFlux --make-link.
// The core owns the link format so every official client exports identical
// profiles. The panel only turns the returned link into a PNG QR code.
type coreShareResult struct {
	Link  string `json:"link"`
	Error string `json:"error"`
	Code  string `json:"code"`
	Param string `json:"param"`
}

var errCoreMakeLinkUnavailable = errors.New("the installed OpenFlux core does not support --make-link")

var shareDNSName = regexp.MustCompile(`(?i)^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)

func shareDirectHost(host string) (string, error) {
	host = strings.TrimSpace(strings.TrimPrefix(strings.TrimSuffix(host, "]"), "["))
	if len(host) > 253 || host == "" || strings.ContainsAny(host, "/\\@?# ") {
		return "", fmt.Errorf("enter the public IP or DNS name of the exit")
	}
	if net.ParseIP(host) != nil {
		return host, nil
	}
	if !shareDNSName.MatchString(host) || strings.Contains(host, "..") {
		return "", fmt.Errorf("invalid exit host")
	}
	return host, nil
}

func buildShareConfig(c Connection, cupsCode, secret, host string) (shareConfig, error) {
	if secret != "" && len(secret) < 16 {
		return shareConfig{}, fmt.Errorf("the encryption key is shorter than OpenFlux share v1 permits")
	}
	negotiated := len(c.Transports) > 0 || c.Negotiate && c.Transport != "cupsonline"
	if negotiated && secret == "" {
		return shareConfig{}, fmt.Errorf("a Session link needs the connection's encryption key")
	}
	if c.Codec != "batched" && c.Codec != "legacy" {
		return shareConfig{}, fmt.Errorf("unsupported codec")
	}
	out := shareConfig{Name: c.Name, Negotiate: negotiated, Secret: secret, Transports: []shareTransport{}}
	if c.Codec != "batched" {
		out.Codec = c.Codec
	}
	links := c.Transports
	if len(links) == 0 {
		links = []TransportLink{{Type: c.Transport, URL: c.URL}}
	}
	if len(c.Transports) > 0 {
		out.Context = sessionContextURL(c)
	} else {
		out.Context = c.URL
	}
	if out.Context == "" {
		out.Context = c.Transport
	}
	counts := map[string]int{}
	for _, link := range links {
		t := shareTransport{Type: link.Type, URL: link.URL, Priority: link.Priority}
		counts[link.Type]++
		if len(c.Transports) > 0 && counts[link.Type] > 1 {
			t.Name = fmt.Sprintf("%s-%d", link.Type, counts[link.Type])
		}
		switch link.Type {
		case "direct":
			if !negotiated {
				return shareConfig{}, fmt.Errorf("Direct needs Session mode before it can be shared")
			}
			validated, err := shareDirectHost(host)
			if err != nil {
				return shareConfig{}, err
			}
			_, port, err := net.SplitHostPort(c.DirectListen)
			if err != nil || port == "" {
				return shareConfig{}, fmt.Errorf("Direct port has not been assigned")
			}
			t.URL = ""
			t.Dial = net.JoinHostPort(validated, port)
		case "cupsonline":
			if t.URL == "" {
				t.URL = cupsCode
			}
			if t.URL == "" {
				return shareConfig{}, fmt.Errorf("Cups.online room code is not available yet; start the connection first")
			}
		case "yandex", "vyandex", "boards", "mailru":
			if t.URL == "" {
				return shareConfig{}, fmt.Errorf("document URL is missing")
			}
		default:
			return shareConfig{}, fmt.Errorf("transport %q is not supported by OpenFlux share v1", link.Type)
		}
		out.Transports = append(out.Transports, t)
	}
	return out, nil
}

func encodeShare(c shareConfig) (shareResult, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return shareResult{}, err
	}
	var packed bytes.Buffer
	w, err := flate.NewWriter(&packed, flate.BestCompression)
	if err != nil {
		return shareResult{}, err
	}
	if _, err := w.Write(raw); err != nil {
		w.Close()
		return shareResult{}, err
	}
	if err := w.Close(); err != nil {
		return shareResult{}, err
	}
	link := "openflux://v1/" + base64.RawURLEncoding.EncodeToString(packed.Bytes())
	qr, err := qrcode.Encode(link, qrcode.Medium, 512)
	if err != nil {
		return shareResult{}, err
	}
	return shareResult{Link: link, QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(qr)}, nil
}

func encodeShareWithCore(binary string, c shareConfig) (shareResult, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return shareResult{}, err
	}
	cmd := exec.Command(binary, "--make-link", "-")
	cmd.Stdin = bytes.NewReader(raw)
	output, runErr := cmd.CombinedOutput()
	var result coreShareResult
	if err := json.Unmarshal(bytes.TrimSpace(output), &result); err != nil {
		if runErr != nil {
			return shareResult{}, errCoreMakeLinkUnavailable
		}
		return shareResult{}, fmt.Errorf("OpenFlux --make-link returned invalid JSON: %w", err)
	}
	if result.Error != "" {
		return shareResult{}, fmt.Errorf("OpenFlux could not create the link: %s", result.Error)
	}
	if runErr != nil || result.Link == "" {
		return shareResult{}, errCoreMakeLinkUnavailable
	}
	qr, err := qrcode.Encode(result.Link, qrcode.Medium, 512)
	if err != nil {
		return shareResult{}, err
	}
	return shareResult{Link: result.Link, QR: "data:image/png;base64," + base64.StdEncoding.EncodeToString(qr)}, nil
}

func (m *Manager) connectionShare(id, host string) (shareResult, error) {
	m.mu.Lock()
	i, ok := m.findLocked(id)
	if !ok {
		m.mu.Unlock()
		return shareResult{}, os.ErrNotExist
	}
	c := m.connections[i]
	secret := ""
	if c.EncryptionKeyFile != "" {
		if c.EncryptionKeyFile != managedKeyPath(id) {
			m.mu.Unlock()
			return shareResult{}, fmt.Errorf("this key is outside the panel; use a panel-managed key to generate a link")
		}
		raw, err := os.ReadFile(c.EncryptionKeyFile)
		if err != nil {
			m.mu.Unlock()
			return shareResult{}, err
		}
		secret = strings.TrimSpace(string(raw))
	}
	cupsCode := ""
	if p := m.processes[id]; p != nil {
		cupsCode = p.clientCode
	}
	binary := m.binaryPath
	m.mu.Unlock()
	spec, err := buildShareConfig(c, cupsCode, secret, host)
	if err != nil {
		return shareResult{}, err
	}
	if binary != "" {
		result, err := encodeShareWithCore(binary, spec)
		if err == nil {
			return result, nil
		}
		if !errors.Is(err, errCoreMakeLinkUnavailable) {
			return shareResult{}, err
		}
	}
	// Older cores do not know --make-link. Preserve QR creation until the
	// administrator applies the server update; every current core uses the
	// canonical path above.
	return encodeShare(spec)
}

