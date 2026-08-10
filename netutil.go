package main

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"time"
)

// addrOpen reports whether a specific address accepts connections. Distinguishing
// 127.0.0.1:80 from 127.0.0.2:80 is the whole diagnosis when two local-dev tools
// share a machine: a wildcard listener answers both, an alias-scoped one only its
// own address.
func addrOpen(addr string, port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("%s:%d", addr, port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// portOpen reports if a local TCP port accepts connections.
func portOpen(port int) bool {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 300*time.Millisecond)
	if err != nil {
		return false
	}
	c.Close()
	return true
}

// freePort asks the kernel for an ephemeral port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitPort polls until the port answers or timeout hits.
func waitPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if portOpen(port) {
			return nil
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("port %d not up after %s", port, timeout)
}

func probeClient() *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// httpProbe fetches a URL, skipping cert verification (self-signed).
func httpProbe(url string) (int, error) {
	resp, err := probeClient().Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}

// httpProbeHost fetches a URL with an explicit Host header — required for
// the shared vhost front where the Host header selects the site.
func httpProbeHost(url, host string) (int, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return 0, err
	}
	req.Host = host
	resp, err := probeClient().Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
