package acp

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"antigravity-gateway/internal/storage"

	"golang.org/x/net/proxy"
)

type EgressRelay struct {
	AccountID   string
	NetworkMode string // "direct" | "proxy" | "bind_ip" | "netns"
	ProxyURL    string
	BindIP      string
	listener    net.Listener
	server      *http.Server
	transport   *http.Transport
	dialContext func(ctx context.Context, network, addr string) (net.Conn, error)
}

func NewEgressRelay(accountID, networkMode, proxyURL, bindIP string) (*EgressRelay, error) {
	networkMode = strings.TrimSpace(networkMode)
	if networkMode == "" || networkMode == "direct" || networkMode == "netns" {
		return nil, nil
	}

	r := &EgressRelay{
		AccountID:   accountID,
		NetworkMode: networkMode,
		ProxyURL:    strings.TrimSpace(proxyURL),
		BindIP:      strings.TrimSpace(bindIP),
	}

	baseDialer := &net.Dialer{
		Timeout:   15 * time.Second,
		KeepAlive: 30 * time.Second,
	}

	switch networkMode {
	case "bind_ip":
		ip := net.ParseIP(r.BindIP)
		if ip == nil {
			return nil, fmt.Errorf("invalid bind_ip %q", r.BindIP)
		}
		baseDialer.LocalAddr = &net.TCPAddr{IP: ip}
		r.dialContext = baseDialer.DialContext
		r.transport = &http.Transport{
			DialContext:           r.dialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
	case "proxy":
		u, err := url.Parse(r.ProxyURL)
		if err != nil || u.Host == "" {
			return nil, fmt.Errorf("invalid proxy_url %q", r.ProxyURL)
		}
		scheme := strings.ToLower(u.Scheme)
		switch scheme {
		case "socks5", "socks5h":
			var auth *proxy.Auth
			if u.User != nil {
				pwd, _ := u.User.Password()
				auth = &proxy.Auth{
					User:     u.User.Username(),
					Password: pwd,
				}
			}
			socksDialer, err := proxy.SOCKS5("tcp", u.Host, auth, baseDialer)
			if err != nil {
				return nil, fmt.Errorf("create socks5 dialer: %w", err)
			}
			r.dialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				if cd, ok := socksDialer.(proxy.ContextDialer); ok {
					return cd.DialContext(ctx, network, addr)
				}
				return socksDialer.Dial(network, addr)
			}
			r.transport = &http.Transport{
				DialContext:         r.dialContext,
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			}
		case "http", "https":
			r.dialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
				conn, err := baseDialer.DialContext(ctx, "tcp", u.Host)
				if err != nil {
					return nil, err
				}
				var authHeader string
				if u.User != nil {
					pwd, _ := u.User.Password()
					cred := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pwd))
					authHeader = "Proxy-Authorization: Basic " + cred + "\r\n"
				}
				connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n%s\r\n", addr, addr, authHeader)
				if _, err := conn.Write([]byte(connectReq)); err != nil {
					_ = conn.Close()
					return nil, err
				}
				br := bufio.NewReader(conn)
				resp, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
				if err != nil {
					_ = conn.Close()
					return nil, err
				}
				if resp.StatusCode != http.StatusOK {
					_ = conn.Close()
					return nil, fmt.Errorf("upstream proxy CONNECT %s returned %s", addr, resp.Status)
				}
				return conn, nil
			}
			r.transport = &http.Transport{
				Proxy:               http.ProxyURL(u),
				DialContext:         baseDialer.DialContext,
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 10 * time.Second,
			}
		default:
			return nil, fmt.Errorf("unsupported proxy scheme %q", scheme)
		}
	default:
		return nil, fmt.Errorf("unsupported network_mode %q", networkMode)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listen local egress relay: %w", err)
	}
	r.listener = ln
	r.server = &http.Server{
		Handler: http.HandlerFunc(r.serveHTTP),
	}
	go func() {
		_ = r.server.Serve(ln)
	}()
	return r, nil
}

func (r *EgressRelay) ListenURL() string {
	if r == nil || r.listener == nil {
		return ""
	}
	return "http://" + r.listener.Addr().String()
}

func (r *EgressRelay) serveHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		targetConn, err := r.dialContext(req.Context(), "tcp", req.Host)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		hj, ok := w.(http.Hijacker)
		if !ok {
			_ = targetConn.Close()
			http.Error(w, "hijacking not supported", http.StatusInternalServerError)
			return
		}
		clientConn, _, err := hj.Hijack()
		if err != nil {
			_ = targetConn.Close()
			return
		}
		_, _ = clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
		go func() {
			defer targetConn.Close()
			defer clientConn.Close()
			_, _ = io.Copy(targetConn, clientConn)
		}()
		go func() {
			defer targetConn.Close()
			defer clientConn.Close()
			_, _ = io.Copy(clientConn, targetConn)
		}()
		return
	}

	outReq := req.Clone(req.Context())
	outReq.RequestURI = ""
	resp, err := r.transport.RoundTrip(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func ValidateProbeURL(rawURL string) error {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" {
		return nil
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" {
		return fmt.Errorf("invalid probe_url %q", rawURL)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return fmt.Errorf("unsupported probe_url scheme %q (must be http or https)", u.Scheme)
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("probe_url target IP %s is not allowed", ip.String())
		}
	} else if strings.EqualFold(host, "metadata.google.internal") {
		return fmt.Errorf("probe_url target host %q is not allowed", host)
	}
	return nil
}

func (r *EgressRelay) ProbeTargetURL(ctx context.Context, targetURL string) (string, error) {
	if strings.TrimSpace(targetURL) == "" {
		targetURL = "https://api.ipify.org"
	}
	if err := ValidateProbeURL(targetURL); err != nil {
		return "", err
	}
	noRedirect := func(req *http.Request, via []*http.Request) error {
		return http.ErrUseLastResponse
	}
	var client *http.Client
	if r == nil || r.ListenURL() == "" {
		client = &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: noRedirect,
		}
	} else {
		proxyU, _ := url.Parse(r.ListenURL())
		tr := &http.Transport{
			Proxy:             http.ProxyURL(proxyU),
			DisableKeepAlives: true,
		}
		defer tr.CloseIdleConnections()
		client = &http.Client{
			Timeout:       10 * time.Second,
			CheckRedirect: noRedirect,
			Transport:     tr,
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, targetURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 256))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

func (r *EgressRelay) Close() error {
	if r == nil || r.server == nil {
		return nil
	}
	return r.server.Close()
}

func BuildIsolatedEnv(baseEnv map[string]string, acc storage.Account, geminiHome string, relayURL string) map[string]string {
	env := make(map[string]string, len(baseEnv)+12)
	for k, v := range baseEnv {
		env[k] = v
	}
	if geminiHome != "" {
		env["GEMINI_HOME"] = geminiHome
		env["GEMINI_CLI_HOME"] = geminiHome
		if acc.IsolationEnabled && acc.ID != "" && acc.ID != "default" {
			parentHome := filepath.Dir(geminiHome)
			if parentHome != "" && parentHome != "." && parentHome != "/" {
				env["HOME"] = parentHome
			}
		}
	}
	if acc.IsolationEnabled && geminiHome != "" {
		env["AGY_ACP_FORCE_FILE_STORAGE"] = "1"
	}
	if acc.AuthMode == "gemini_api_key" && strings.TrimSpace(acc.APIKey) != "" {
		env["GEMINI_API_KEY"] = strings.TrimSpace(acc.APIKey)
	}
	if strings.TrimSpace(acc.GCPProject) != "" {
		env["GOOGLE_CLOUD_PROJECT"] = strings.TrimSpace(acc.GCPProject)
	}
	if strings.TrimSpace(acc.GCPLocation) != "" {
		env["GOOGLE_CLOUD_LOCATION"] = strings.TrimSpace(acc.GCPLocation)
	}
	if relayURL != "" {
		env["HTTP_PROXY"] = relayURL
		env["HTTPS_PROXY"] = relayURL
		env["http_proxy"] = relayURL
		env["https_proxy"] = relayURL
		noProxy := "127.0.0.1,localhost,::1"
		env["NO_PROXY"] = noProxy
		env["no_proxy"] = noProxy
	}
	return env
}
