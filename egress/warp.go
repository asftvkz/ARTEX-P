package egress

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

var warpIDPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,47}$`)

type WarpProfile struct {
	ID                    string `json:"id"`
	Name                  string `json:"name"`
	Phase                 string `json:"phase"`
	Desired               bool   `json:"desired"`
	Port                  int    `json:"port"`
	EgressIP              string `json:"egress_ip,omitempty"`
	LastError             string `json:"last_error,omitempty"`
	RegistrationAttempted bool   `json:"registration_attempted"`
	Registered            bool   `json:"registered"`
	Generated             bool   `json:"generated"`
}

type WarpConnection struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"-"`
	Password string `json:"-"`
}

type warpAuth struct {
	Username string `json:"username"`
	Password string `json:"password"`
}
type warpProcess struct {
	cmd      *exec.Cmd
	done     chan struct{}
	stopping bool
}

type WarpManager struct {
	dir       string
	mu        sync.Mutex
	processes map[string]*warpProcess
	run       func(context.Context, string, []string, string) error
	probe     func(context.Context, WarpConnection) (string, error)
}

func NewWarpManager(dir string) (*WarpManager, error) {
	if dir == "" {
		return nil, errors.New("WARP 数据目录不能为空")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	return &WarpManager{dir: dir, processes: make(map[string]*warpProcess), run: runWarpCommand, probe: probeWarpEgress}, nil
}

func runWarpCommand(ctx context.Context, exe string, args []string, dir string) error {
	// 子进程先设置私有 umask，避免注册材料曾短暂以 0644 出现。
	argv := append([]string{"-c", `umask 077; exec "$@"`, "sh", exe}, args...)
	cmd := exec.CommandContext(ctx, "sh", argv...)
	cmd.Dir = dir
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return errors.New("WARP 工具命令失败，原始输出已隐藏")
	}
	return nil
}

func (m *WarpManager) path(id string) (string, error) {
	if !warpIDPattern.MatchString(id) {
		return "", errors.New("WARP 档案编号无效")
	}
	return filepath.Join(m.dir, id), nil
}

func privateWrite(path string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err = os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	if err = os.Chmod(tmp, 0600); err != nil {
		return err
	}
	if err = os.Rename(tmp, path); err != nil {
		return err
	}
	return nil
}

func readProfile(path string) (WarpProfile, error) {
	var p WarpProfile
	data, err := os.ReadFile(filepath.Join(path, "meta.json"))
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(data, &p); err != nil {
		return p, errors.New("WARP 档案状态无效")
	}
	if !warpIDPattern.MatchString(p.ID) || filepath.Base(path) != p.ID {
		return p, errors.New("WARP 档案编号不匹配")
	}
	return p, nil
}

func (m *WarpManager) save(path string, p WarpProfile) error {
	return privateWrite(filepath.Join(path, "meta.json"), p)
}

func (m *WarpManager) Get(id string) (WarpProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.path(id)
	if err != nil {
		return WarpProfile{}, err
	}
	p, err := readProfile(path)
	return m.liveProfileLocked(p), err
}

// 磁盘的 ready 只是上次探测结果；本进程没有活跃运行器时不能宣称在线。
func (m *WarpManager) liveProfileLocked(p WarpProfile) WarpProfile {
	if p.Phase != "ready" {
		p.EgressIP = ""
		return p
	}
	if !m.runningLocked(p.ID) || !p.Desired || net.ParseIP(p.EgressIP) == nil {
		p.Phase, p.EgressIP, p.LastError = "failed", "", "WARP 运行器未在线或出口未验证"
	}
	return p
}

func (m *WarpManager) runningLocked(id string) bool {
	proc := m.processes[id]
	if proc == nil {
		return false
	}
	select {
	case <-proc.done:
		return false
	default:
		return true
	}
}

func (m *WarpManager) List() ([]WarpProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return nil, err
	}
	list := make([]WarpProfile, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !warpIDPattern.MatchString(entry.Name()) {
			continue
		}
		p, err := readProfile(filepath.Join(m.dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		list = append(list, m.liveProfileLocked(p))
	}
	return list, nil
}

func secret() (string, error) {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf[:]), nil
}

func (m *WarpManager) allocatePort() (int, error) {
	used := map[int]bool{}
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return 0, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if p, e := readProfile(filepath.Join(m.dir, entry.Name())); e == nil {
				used[p.Port] = true
			}
		}
	}
	for i := 0; i < 32; i++ {
		listener, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			return 0, e
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		if port >= 1024 && !used[port] {
			return port, nil
		}
	}
	return 0, errors.New("无法分配 WARP 本地端口")
}

// Create 在执行任何外部注册命令前持久化注册意图。重复请求只返回原档案，绝不重新注册。
func (m *WarpManager) Create(ctx context.Context, id, name string, acceptTerms bool) (WarpProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.path(id)
	if err != nil {
		return WarpProfile{}, err
	}
	if !acceptTerms {
		return WarpProfile{}, errors.New("请先阅读并接受 Cloudflare WARP 条款")
	}
	if _, e := os.Stat(path); e == nil {
		return readProfile(path)
	} else if !os.IsNotExist(e) {
		return WarpProfile{}, e
	}
	if strings.TrimSpace(name) == "" {
		name = id
	}
	if len(name) > 80 {
		return WarpProfile{}, errors.New("WARP 名称过长")
	}
	port, err := m.allocatePort()
	if err != nil {
		return WarpProfile{}, err
	}
	username, err := secret()
	if err != nil {
		return WarpProfile{}, err
	}
	password, err := secret()
	if err != nil {
		return WarpProfile{}, err
	}
	if err = os.Mkdir(path, 0700); err != nil {
		return WarpProfile{}, err
	}
	if err = privateWrite(filepath.Join(path, "auth.json"), warpAuth{username, password}); err != nil {
		_ = os.RemoveAll(path)
		return WarpProfile{}, err
	}
	p := WarpProfile{ID: id, Name: name, Phase: "registering", Desired: true, Port: port, RegistrationAttempted: true}
	if err = m.save(path, p); err != nil {
		_ = os.RemoveAll(path)
		return WarpProfile{}, err
	}
	return m.finish(ctx, path, p)
}

func (m *WarpManager) finish(ctx context.Context, path string, p WarpProfile) (WarpProfile, error) {
	fail := func(message string, e error) (WarpProfile, error) {
		p.Phase = "failed"
		p.LastError = message
		_ = m.save(path, p)
		return p, e
	}
	if !p.Registered {
		// 不明确的注册结果只能人工核对；Resume 不能重新提交注册。
		if p.Phase != "registering" {
			return fail("注册结果不明确，需人工核对", errors.New("WARP 注册结果不明确，拒绝重复注册"))
		}
		accountFile := filepath.Join(path, "wgcf-account.toml")
		if err := m.run(ctx, "wgcf", []string{"--config", accountFile, "register", "--accept-tos"}, path); err != nil {
			if _, statErr := os.Stat(accountFile); statErr == nil {
				_ = os.Chmod(accountFile, 0600)
			}
			return fail("注册结果不明确，需人工核对", err)
		}
		if err := os.Chmod(accountFile, 0600); err != nil {
			return fail("注册材料权限设置失败", err)
		}
		p.Registered = true
		p.Phase = "generating"
		if err := m.save(path, p); err != nil {
			return p, err
		}
	}
	if !p.Generated {
		generated := filepath.Join(path, "generated.conf")
		if err := m.run(ctx, "wgcf", []string{"--config", filepath.Join(path, "wgcf-account.toml"), "generate", "--profile", generated}, path); err != nil {
			return fail("WARP 配置生成失败", err)
		}
		if err := os.Chmod(generated, 0600); err != nil {
			return fail("WARP 配置权限设置失败", err)
		}
		data, err := os.ReadFile(generated)
		if err != nil {
			return fail("WARP 配置读取失败", err)
		}
		if !strings.Contains(string(data), "[Interface]") || !strings.Contains(string(data), "[Peer]") {
			return fail("WARP 配置结构无效", errors.New("WARP 配置结构无效"))
		}
		var auth warpAuth
		if err := readAuth(path, &auth); err != nil {
			return fail("WARP 认证材料无效", err)
		}
		body := strings.TrimSpace(string(data)) + fmt.Sprintf("\n\n[Socks5]\nBindAddress = 127.0.0.1:%d\nUsername = %s\nPassword = %s\n", p.Port, auth.Username, auth.Password)
		if err := os.WriteFile(filepath.Join(path, "wireproxy.conf"), []byte(body), 0600); err != nil {
			return fail("WARP 代理配置写入失败", err)
		}
		if err := os.Chmod(filepath.Join(path, "wireproxy.conf"), 0600); err != nil {
			return fail("WARP 代理配置权限设置失败", err)
		}
		if err := m.run(ctx, "wireproxy", []string{"-n", "-c", filepath.Join(path, "wireproxy.conf")}, path); err != nil {
			return fail("WARP 代理配置验证失败", err)
		}
		p.Generated = true
		p.Phase = "starting"
		if err := m.save(path, p); err != nil {
			return p, err
		}
	}
	return m.startLocked(ctx, path, p)
}

func readAuth(path string, auth *warpAuth) error {
	data, err := os.ReadFile(filepath.Join(path, "auth.json"))
	if err != nil {
		return err
	}
	if err = json.Unmarshal(data, auth); err != nil {
		return err
	}
	if auth.Username == "" || auth.Password == "" {
		return errors.New("认证材料缺失")
	}
	return nil
}

func (m *WarpManager) Connection(id string) (WarpConnection, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.path(id)
	if err != nil {
		return WarpConnection{}, err
	}
	p, err := readProfile(path)
	if err != nil {
		return WarpConnection{}, err
	}
	if p.Phase != "ready" || !p.Desired || p.EgressIP == "" {
		return WarpConnection{}, errors.New("WARP 出口尚未验证可用")
	}
	if m.liveProfileLocked(p).Phase != "ready" {
		return WarpConnection{}, errors.New("WARP 运行器未在线")
	}
	var auth warpAuth
	if err := readAuth(path, &auth); err != nil {
		return WarpConnection{}, err
	}
	return WarpConnection{Host: "127.0.0.1", Port: p.Port, Username: auth.Username, Password: auth.Password}, nil
}

func (m *WarpManager) startLocked(ctx context.Context, path string, p WarpProfile) (WarpProfile, error) {
	if running := m.processes[p.ID]; running != nil {
		if current := m.liveProfileLocked(p); current.Phase == "ready" {
			return current, nil
		}
		return p, errors.New("WARP 出口已有运行进程")
	}
	p.Desired = true
	p.Phase = "starting"
	p.LastError = ""
	p.EgressIP = ""
	if err := m.save(path, p); err != nil {
		return p, err
	}
	cmd := exec.Command("wireproxy", "-c", filepath.Join(path, "wireproxy.conf"))
	cmd.Dir = path
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		p.Phase = "failed"
		p.LastError = "wireproxy 启动失败"
		_ = m.save(path, p)
		return p, errors.New(p.LastError)
	}
	proc := &warpProcess{cmd: cmd, done: make(chan struct{})}
	m.processes[p.ID] = proc
	go m.watch(path, p.ID, proc)
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	for {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", p.Port), 150*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		select {
		case <-ctx.Done():
			m.stopProcessLocked(p.ID)
			p.Phase = "failed"
			p.LastError = "WARP 启动取消"
			_ = m.save(path, p)
			return p, ctx.Err()
		case <-proc.done:
			p.Phase = "failed"
			p.LastError = "wireproxy 启动后退出"
			_ = m.save(path, p)
			return p, errors.New(p.LastError)
		case <-deadline.C:
			m.stopProcessLocked(p.ID)
			p.Phase = "failed"
			p.LastError = "WARP 本地监听超时"
			_ = m.save(path, p)
			return p, errors.New(p.LastError)
		case <-time.After(100 * time.Millisecond):
		}
	}
	var auth warpAuth
	if err := readAuth(path, &auth); err != nil {
		m.stopProcessLocked(p.ID)
		return p, err
	}
	ip, err := m.probe(ctx, WarpConnection{"127.0.0.1", p.Port, auth.Username, auth.Password})
	if err != nil {
		m.stopProcessLocked(p.ID)
		p.Phase = "failed"
		p.LastError = "WARP 联网检测失败"
		_ = m.save(path, p)
		return p, errors.New(p.LastError)
	}
	p.Phase = "ready"
	p.EgressIP = ip
	if err := m.save(path, p); err != nil {
		return p, err
	}
	return p, nil
}

func (m *WarpManager) watch(path, id string, proc *warpProcess) {
	_ = proc.cmd.Wait()
	close(proc.done)
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.processes[id] != proc {
		return
	}
	delete(m.processes, id)
	if proc.stopping {
		return
	}
	p, err := readProfile(path)
	if err != nil {
		return
	}
	p.Phase = "failed"
	p.EgressIP = ""
	p.LastError = "wireproxy 进程已退出"
	_ = m.save(path, p)
}

func (m *WarpManager) stopProcessLocked(id string) {
	proc := m.processes[id]
	if proc == nil {
		return
	}
	proc.stopping = true
	if proc.cmd.Process != nil {
		_ = proc.cmd.Process.Kill()
	}
	select {
	case <-proc.done:
	case <-time.After(3 * time.Second):
	}
	delete(m.processes, id)
}

func (m *WarpManager) Start(ctx context.Context, id string) (WarpProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.path(id)
	if err != nil {
		return WarpProfile{}, err
	}
	p, err := readProfile(path)
	if err != nil {
		return p, err
	}
	if !p.Registered || !p.Generated {
		return p, errors.New("WARP 档案尚未完成创建，请恢复")
	}
	return m.startLocked(ctx, path, p)
}

// Probe 刷新当前出口 IP；停止、崩溃或失败的运行器不会仅凭旧地址恢复为 healthy。
func (m *WarpManager) Probe(ctx context.Context, id string) (WarpProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.path(id)
	if err != nil {
		return WarpProfile{}, err
	}
	p, err := readProfile(path)
	if err != nil {
		return p, err
	}
	p = m.liveProfileLocked(p)
	// 失败的探测不销毁仍在服务任务的进程；材料与进程有效即可原地复探。
	if (p.Phase != "ready" && p.Phase != "failed") || !p.Desired || !p.Registered || !p.Generated || !m.runningLocked(id) {
		return p, errors.New("WARP 出口尚未就绪")
	}
	var auth warpAuth
	if err := readAuth(path, &auth); err != nil {
		return p, err
	}
	ip, err := m.probe(ctx, WarpConnection{"127.0.0.1", p.Port, auth.Username, auth.Password})
	if err != nil {
		p.Phase, p.EgressIP, p.LastError = "failed", "", "WARP 联网检测失败"
		// 健康探测不拥有生命周期准入权；停止仍须走管理端引用屏障。
	} else {
		p.Phase, p.EgressIP, p.LastError = "ready", ip, ""
	}
	if saveErr := m.save(path, p); saveErr != nil {
		return p, saveErr
	}
	if err != nil {
		return p, errors.New(p.LastError)
	}
	return p, nil
}

func (m *WarpManager) Resume(ctx context.Context, id string) (WarpProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	path, err := m.path(id)
	if err != nil {
		return WarpProfile{}, err
	}
	p, err := readProfile(path)
	if err != nil {
		return p, err
	}
	if p.Phase == "ready" {
		return p, nil
	}
	if p.RegistrationAttempted && !p.Registered {
		return p, errors.New("WARP 注册结果不明确，拒绝重复注册")
	}
	return m.finish(ctx, path, p)
}

func (m *WarpManager) Stop(ctx context.Context, id string) (WarpProfile, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return WarpProfile{}, err
	}
	path, err := m.path(id)
	if err != nil {
		return WarpProfile{}, err
	}
	p, err := readProfile(path)
	if err != nil {
		return p, err
	}
	p.Desired = false
	p.Phase = "stopped"
	p.EgressIP = ""
	p.LastError = ""
	if err = m.save(path, p); err != nil {
		return p, err
	}
	m.stopProcessLocked(id)
	return p, nil
}

func (m *WarpManager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := m.path(id)
	if err != nil {
		return err
	}
	p, err := readProfile(path)
	if err != nil {
		return err
	}
	// 管理端已检查引用；删除直接停止出口，先保存停止意图以免中断后自动恢复。
	p.Desired, p.Phase, p.EgressIP, p.LastError = false, "stopped", "", ""
	if err := m.save(path, p); err != nil {
		return err
	}
	m.stopProcessLocked(id)
	return os.RemoveAll(path)
}

func (m *WarpManager) Restore(ctx context.Context) error {
	list, err := m.List()
	if err != nil {
		return err
	}
	for _, p := range list {
		if p.Desired && p.Registered && p.Generated {
			if _, err := m.Start(ctx, p.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (m *WarpManager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id := range m.processes {
		m.stopProcessLocked(id)
	}
	return nil
}

func probeWarpEgress(ctx context.Context, c WarpConnection) (string, error) {
	u := &url.URL{Scheme: "socks5", Host: fmt.Sprintf("%s:%d", c.Host, c.Port), User: url.UserPassword(c.Username, c.Password)}
	tr := &http.Transport{Proxy: http.ProxyURL(u), TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: 8 * time.Second}
	defer tr.CloseIdleConnections()
	client := &http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://cloudflare.com/cdn-cgi/trace", nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", errors.New("WARP 出口检测连接失败")
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", errors.New("WARP 出口检测响应异常")
	}
	reader := bufio.NewScanner(io.LimitReader(resp.Body, 4097))
	values := map[string]string{}
	for reader.Scan() {
		key, value, ok := strings.Cut(reader.Text(), "=")
		if ok {
			values[key] = value
		}
	}
	if reader.Err() != nil {
		return "", errors.New("WARP 出口检测读取失败")
	}
	ip := net.ParseIP(values["ip"])
	if ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return "", errors.New("WARP 出口未返回公网 IP")
	}
	if values["warp"] != "on" && values["warp"] != "plus" {
		return "", errors.New("WARP 出口未报告已启用")
	}
	return ip.String(), nil
}
