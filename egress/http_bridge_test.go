package egress

import (
	"bufio"
	"encoding/base64"
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

func TestHTTPBridgeAuthenticatedSOCKSAndConnect(t *testing.T) {
	var direct, authenticated atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { direct.Add(1) }))
	defer target.Close()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	errors := make(chan error, 2)
	go func() {
		for i := 0; i < 2; i++ {
			c, err := ln.Accept()
			if err != nil {
				errors <- err
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				read := func(n int) ([]byte, error) { b := make([]byte, n); _, err := io.ReadFull(c, b); return b, err }
				head, err := read(2)
				if err != nil {
					errors <- err
					return
				}
				if _, err = read(int(head[1])); err != nil {
					errors <- err
					return
				}
				c.Write([]byte{5, 2})
				auth, err := read(2)
				if err != nil {
					errors <- err
					return
				}
				username, err := read(int(auth[1]))
				if err != nil {
					errors <- err
					return
				}
				length, err := read(1)
				if err != nil {
					errors <- err
					return
				}
				password, err := read(int(length[0]))
				if err != nil {
					errors <- err
					return
				}
				if string(username) != "user" || string(password) != "secret" {
					errors <- fmt.Errorf("SOCKS认证凭据错误")
					return
				}
				authenticated.Add(1)
				c.Write([]byte{1, 0})
				connect, err := read(4)
				if err != nil {
					errors <- err
					return
				}
				n := 4
				switch connect[3] {
				case 3:
					b, err := read(1)
					if err != nil {
						errors <- err
						return
					}
					n = int(b[0])
				case 4:
					n = 16
				}
				if _, err = read(n + 2); err != nil {
					errors <- err
					return
				}
				c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
				req, err := http.ReadRequest(bufio.NewReader(c))
				if err != nil {
					errors <- err
					return
				}
				req.Body.Close()
				fmt.Fprint(c, "HTTP/1.1 200 OK\r\nContent-Length: 6\r\nConnection: close\r\n\r\nSOCKS!")
				errors <- nil
			}()
		}
	}()
	bridge, err := NewHTTPBridge("socks5://user:secret@" + ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	u, _ := url.Parse(bridge.ProxyAddr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 5 * time.Second}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(b) != "SOCKS!" {
		t.Fatal("HTTP桥未走SOCKS")
	}
	c, err := net.DialTimeout("tcp", u.Host, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
	pw, _ := u.User.Password()
	authorization := base64.StdEncoding.EncodeToString([]byte(u.User.Username() + ":" + pw))
	fmt.Fprintf(c, "CONNECT example.invalid:443 HTTP/1.1\r\nHost: example.invalid:443\r\nProxy-Authorization: Basic %s\r\n\r\n", authorization)
	reader := bufio.NewReader(c)
	response, err := http.ReadResponse(reader, &http.Request{Method: "CONNECT"})
	if err != nil || response.StatusCode != 200 {
		t.Fatalf("CONNECT失败 %v", err)
	}
	fmt.Fprint(c, "GET / HTTP/1.1\r\nHost: example.invalid\r\n\r\n")
	response, err = http.ReadResponse(reader, nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(response.Body)
	response.Body.Close()
	if string(b) != "SOCKS!" {
		t.Fatal("CONNECT桥未走SOCKS")
	}
	for i := 0; i < 2; i++ {
		if err := <-errors; err != nil {
			t.Fatal(err)
		}
	}
	if direct.Load() != 0 || authenticated.Load() != 2 {
		t.Fatal("SOCKS认证/隔离契约不成立")
	}
	// 无入口身份不能使用任务出口。
	u.User = nil
	client.Transport = &http.Transport{Proxy: http.ProxyURL(u)}
	response, err = client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 407 {
		t.Fatal("缺失身份未闭锁")
	}
}

func TestHTTPBridgeFailureNeverDialsTarget(t *testing.T) {
	var hits atomic.Int64
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer target.Close()
	bridge, err := NewHTTPBridge("socks5://user:secret@127.0.0.1:1")
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	u, _ := url.Parse(bridge.ProxyAddr())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 5 * time.Second}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 502 || hits.Load() != 0 || strings.Contains(string(b), "secret") {
		t.Fatal("HTTP桥失败直连或泄密")
	}
}
