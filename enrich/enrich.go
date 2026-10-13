// Package enrich 对任务登记的域名和 HTTP 资产进行补全。
// DNS-over-HTTPS 和站点探测均使用任务出口，正式操作等待补全退出后才能回收代理。
package enrich

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Autumn-27/artex/db"
	"github.com/Autumn-27/artex/egress"
)

type jobKind int

const (
	jobDNS  jobKind = iota // resolve a domain
	jobHTTP                // probe a web asset (site)
)

type job struct {
	kind   jobKind
	id     int64  // asset id (domain for DNS, site for HTTP)
	arg    string // host (DNS) or url (HTTP)
	taskID int64
	ctx    context.Context
	done   func()
}

// RouteResolver 从任务的正式操作上下文解析出口；出错必须停止探测。
type Route struct{ ProxyURL, CACertPath string }
type RouteResolver func(context.Context, int64) (Route, error)

// Engine 管理有界探测队列，按每个任务独立解析出口和创建客户端。
type Engine struct {
	as       *db.AssetStore
	route    RouteResolver
	dnsURL   string
	ctx      context.Context
	cancel   context.CancelFunc
	mu       sync.Mutex
	stopping bool
	workers  sync.WaitGroup

	jobs chan job
	cool sync.Map // dedup/cooldown: "kind:id" -> time.Time (last run)
	once sync.Once
}

const (
	cooldown    = 5 * time.Minute
	httpTimeout = 12 * time.Second
	queueSize   = 1024
)

// New 创建探测引擎；route 缺失或返回错误时禁止网络访问。
func New(as *db.AssetStore, route RouteResolver, workers int) *Engine {
	if workers <= 0 {
		workers = 4
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &Engine{
		as:    as,
		route: route, ctx: ctx, cancel: cancel,
		// IP 字面量避免为 DNS 查询端点再走本机解析器。
		dnsURL: "https://1.1.1.1/dns-query",
		jobs:   make(chan job, queueSize),
	}
	for i := 0; i < workers; i++ {
		e.workers.Add(1)
		go e.worker()
	}
	return e
}

// buildClient 不读取环境代理。DNS 校验证书并信任任务录制 CA；
// 站点探测保留对自签名目标证书的兼容行为。
func buildClient(route Route, verifyTLS bool) (*http.Client, error) {
	tlsConfig := &tls.Config{InsecureSkipVerify: !verifyTLS}
	if verifyTLS && route.CACertPath != "" {
		pem, err := os.ReadFile(route.CACertPath)
		if err != nil {
			return nil, errors.New("无法读取探测代理 CA")
		}
		roots, err := x509.SystemCertPool()
		if err != nil {
			roots = x509.NewCertPool()
		}
		if !roots.AppendCertsFromPEM(pem) {
			return nil, errors.New("探测代理 CA 无效")
		}
		tlsConfig.RootCAs = roots
	}
	tr := &http.Transport{
		TLSClientConfig:     tlsConfig,
		DisableKeepAlives:   true,
		TLSHandshakeTimeout: httpTimeout,
	}
	if route.ProxyURL != "" {
		u, err := egress.ProxyURL(route.ProxyURL)
		if err != nil {
			return nil, err
		}
		tr.Proxy = http.ProxyURL(u)
	}
	return &http.Client{
		Transport: tr,
		Timeout:   httpTimeout,
		// 不跟随重定向，避免探测扩展到登记目标以外。
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}, nil
}

// Scope 将任务身份与探测完成等待绑定在同一次 Worker 操作内。
type Scope struct {
	engine *Engine
	ctx    context.Context
	cancel context.CancelFunc
	taskID int64
	mu     sync.Mutex
	closed bool
	jobs   sync.WaitGroup
}

func (e *Engine) ForTask(ctx context.Context, taskID int64) *Scope {
	if ctx == nil {
		ctx = context.Background()
	}
	child, cancel := context.WithCancel(ctx)
	return &Scope{engine: e, ctx: child, cancel: cancel, taskID: taskID}
}
func (s *Scope) ResolveDomain(id int64, host string) { s.enqueue(jobDNS, id, host) }

// ProbeSite enqueues an HTTP probe for a web-asset (site). rawURL is the site URL.
func (s *Scope) ProbeSite(id int64, rawURL string) { s.enqueue(jobHTTP, id, rawURL) }
func (s *Scope) enqueue(kind jobKind, id int64, arg string) {
	if s == nil || s.engine == nil || s.ctx == nil || s.ctx.Err() != nil || s.taskID <= 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.jobs.Add(1)
	s.engine.enqueue(job{kind: kind, id: id, arg: arg, taskID: s.taskID, ctx: s.ctx, done: s.jobs.Done})
}
func (s *Scope) Close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	// 长任务可以持续登记资产；仅收尾等待限制为 30 秒。
	timer := time.AfterFunc(30*time.Second, s.cancel)
	defer timer.Stop()
	s.jobs.Wait()
	s.cancel()
}

func (e *Engine) enqueue(j job) {
	if e == nil || id0(j.id) || j.arg == "" {
		j.done()
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.stopping {
		j.done()
		return
	}
	select {
	case e.jobs <- j:
	default: // queue full → drop (best-effort enrichment)
		j.done()
		log.Printf("[enrich] 队列已满，丢弃任务 kind=%d id=%d", j.kind, j.id)
	}
}

func id0(id int64) bool { return id <= 0 }

// Close stops the workers (idempotent).
func (e *Engine) Close() {
	if e == nil {
		return
	}
	e.once.Do(func() { e.mu.Lock(); e.stopping = true; e.cancel(); close(e.jobs); e.mu.Unlock() })
	e.workers.Wait()
}

func (e *Engine) worker() {
	defer e.workers.Done()
	for j := range e.jobs {
		e.run(j)
	}
}
func (e *Engine) run(j job) {
	defer j.done()
	if e.ctx.Err() != nil || j.ctx.Err() != nil || e.route == nil || e.onCooldown(j) {
		return
	}
	ctx, cancel := context.WithTimeout(j.ctx, httpTimeout)
	stop := context.AfterFunc(e.ctx, cancel)
	defer cancel()
	defer stop()
	route, err := e.route(ctx, j.taskID)
	if err != nil {
		log.Printf("[enrich] task=%d asset=%d 出口不可用，停止探测", j.taskID, j.id)
		return
	}
	client, err := buildClient(route, j.kind == jobDNS)
	if err != nil {
		log.Printf("[enrich] task=%d asset=%d 探测客户端不可用", j.taskID, j.id)
		return
	}
	defer client.CloseIdleConnections()
	if j.kind == jobDNS {
		e.doDNS(ctx, client, j)
	} else {
		e.doHTTP(ctx, client, j)
	}
}

// 同一资产在不同任务中分别探测，避免使用另一任务的出口结果代替本次探测。
func (e *Engine) onCooldown(j job) bool {
	key := itoa(j.taskID) + ":" + string(rune(j.kind)) + ":" + itoa(j.id)
	for {
		now := time.Now()
		previous, loaded := e.cool.LoadOrStore(key, now)
		if !loaded {
			return false
		}
		if time.Since(previous.(time.Time)) < cooldown {
			return true
		}
		if e.cool.CompareAndSwap(key, previous, now) {
			return false
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// ---- DNS ----

func (e *Engine) doDNS(ctx context.Context, client *http.Client, j job) {
	host := j.arg
	data, err := queryDNS(ctx, client, e.dnsURL, host)
	if err != nil || data == nil || ctx.Err() != nil {
		return
	}
	ips := uniq(append(append([]string{}, data.A...), data.AAAA...))
	// Upsert resolved IPs into the asset store.
	for _, ip := range ips {
		_, _ = e.as.UpsertIP(db.UpsertIPReq{
			IP:           ip,
			BoundDomains: []string{host},
			TaskID:       j.taskID,
		})
	}
	// Record A records as subdomains if host looks like a subdomain.
	for _, a := range data.A {
		_, _ = e.as.UpsertSubdomain(db.UpsertSubdomainReq{
			Domain:      host,
			RecordType:  "A",
			RecordValue: []string{a},
			TaskID:      j.taskID,
		})
	}
	for _, aaaa := range data.AAAA {
		_, _ = e.as.UpsertSubdomain(db.UpsertSubdomainReq{
			Domain:      host,
			RecordType:  "AAAA",
			RecordValue: []string{aaaa},
			TaskID:      j.taskID,
		})
	}
	for _, cname := range data.CNAME {
		_, _ = e.as.UpsertSubdomain(db.UpsertSubdomainReq{
			Domain:      host,
			RecordType:  "CNAME",
			RecordValue: []string{cname},
			TaskID:      j.taskID,
		})
	}
}

// ---- HTTP probe ----

var reTitle = regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`)

func (e *Engine) doHTTP(ctx context.Context, client *http.Client, j job) {
	rawURL := j.arg
	host := hostOf(rawURL)
	if host == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, "GET", rawURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("User-Agent", "artex-enrich/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // cap 1 MiB
	if err != nil || ctx.Err() != nil {
		return
	}
	statusCode := resp.StatusCode
	bodyLen := int64(len(body))
	title := extractTitle(body)
	_, _ = e.as.UpsertHTTPService(db.UpsertHTTPServiceReq{
		URL:           rawURL,
		StatusCode:    &statusCode,
		ContentLength: &bodyLen,
		PageTitle:     title,
		TaskID:        j.taskID,
	})
}

func extractTitle(body []byte) string {
	m := reTitle.FindSubmatch(body)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(string(m[1])))
}

func hostOf(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	return u.Hostname()
}

func isBlank(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case float64:
		return x == 0
	case int:
		return x == 0
	}
	return false
}

func uniq(in []string) []string {
	seen := map[string]struct{}{}
	out := in[:0]
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
