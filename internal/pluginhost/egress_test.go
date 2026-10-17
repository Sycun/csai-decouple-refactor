package pluginhost

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestProxyRedirectRequiresSeparateTargetApproval(t *testing.T) {
	for _, approveRedirect := range []bool{false, true} {
		t.Run(fmt.Sprint(approveRedirect), func(t *testing.T) {
			var reached atomic.Bool
			second := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached.Store(true)
				fmt.Fprint(w, "redirect-target")
			}))
			defer second.Close()
			first := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, second.URL, http.StatusFound)
			}))
			defer first.Close()
			ports := []int{portOf(t, strings.TrimPrefix(first.URL, "http://"))}
			if approveRedirect {
				ports = append(ports, portOf(t, strings.TrimPrefix(second.URL, "http://")))
			}
			p, err := NewProxy(NewAllowlist([]string{"net.connect(target)"}, []Tuple{{Host: "127.0.0.1", Ports: ports, Method: "GET"}}, true))
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			pu, _ := url.Parse("http://" + p.Addr())
			transport := &http.Transport{Proxy: http.ProxyURL(pu)}
			defer transport.CloseIdleConnections()
			c := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			r, err := c.Get(first.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Body.Close()
			want := http.StatusForbidden
			if approveRedirect {
				want = http.StatusOK
			}
			if r.StatusCode != want || reached.Load() != approveRedirect {
				t.Fatalf("status=%d reached=%v, approved=%v", r.StatusCode, reached.Load(), approveRedirect)
			}
		})
	}
}

type proxyRoundTripper func(*http.Request) (*http.Response, error)

func (f proxyRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestProxyChecksActualHTTPDestinationPort(t *testing.T) {
	for _, tc := range []struct {
		url, host string
		port      int
	}{
		{"http://example.invalid/path", "example.invalid", 80},
		{"https://example.invalid/path", "example.invalid", 443},
		{"http://example.invalid:8080/path", "example.invalid", 8080},
		{"http://[::1]/path", "::1", 80},
	} {
		t.Run(tc.url, func(t *testing.T) {
			for _, port := range []int{tc.port, tc.port + 1} {
				called := false
				p := &Proxy{allowlist: NewAllowlist([]string{"net.connect(target)"}, []Tuple{{Host: tc.host, Ports: []int{port}, Method: "GET"}}, true),
					client: &http.Client{Transport: proxyRoundTripper(func(r *http.Request) (*http.Response, error) {
						called = true
						return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("ok"))}, nil
					})}}
				rec := httptest.NewRecorder()
				p.handle(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
				if called != (port == tc.port) {
					t.Fatalf("approved port=%d actual=%d called=%v", port, tc.port, called)
				}
				want := http.StatusForbidden
				if port == tc.port {
					want = http.StatusOK
				}
				if rec.Code != want {
					t.Fatalf("status=%d want=%d", rec.Code, want)
				}
			}
		})
	}
}

func TestProxyTunnelClosesBothPeers(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint(shutdown), func(t *testing.T) {
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer l.Close()
			accepted := make(chan net.Conn, 1)
			finished := make(chan struct{})
			go func() {
				defer close(finished)
				c, err := l.Accept()
				if err != nil {
					return
				}
				accepted <- c
				io.Copy(c, c)
			}()
			p, err := NewProxy(NewAllowlist([]string{"net.connect(target)"}, []Tuple{{Host: "127.0.0.1", Ports: []int{portOf(t, l.Addr().String())}, Method: "CONNECT"}}, true))
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			c, err := net.DialTimeout("tcp", p.Addr(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			c.SetDeadline(time.Now().Add(2 * time.Second))
			// The payload can already be buffered when Hijack returns.
			fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nping", l.Addr(), l.Addr())
			reader := bufio.NewReader(c)
			r, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
			if err != nil || r.StatusCode != http.StatusOK {
				t.Fatalf("CONNECT: %v", err)
			}
			var upstream net.Conn
			select {
			case upstream = <-accepted:
			case <-time.After(time.Second):
				t.Fatal("upstream not connected")
			}
			defer upstream.Close()
			buf := make([]byte, 4)
			if _, err := io.ReadFull(reader, buf); err != nil || string(buf) != "ping" {
				t.Fatalf("buffered payload: %q %v", buf, err)
			}
			if shutdown {
				if err := p.Close(); err != nil {
					t.Fatal(err)
				}
				if _, err := c.Write([]byte("more")); err == nil {
					if _, err := io.ReadFull(reader, buf); err == nil {
						t.Fatal("closed proxy still forwards tunnel traffic")
					}
				}
			} else {
				c.Close()
			}
			select {
			case <-finished:
			case <-time.After(time.Second):
				t.Fatal("upstream stayed open after tunnel closed")
			}
		})
	}
}
