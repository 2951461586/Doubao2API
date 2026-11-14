// Doubao2API 把本机豆包（DoubaoWork）桌面端的上游模型服务，
// 逆向复刻成一个 OpenAI 兼容的 Go 网关。
//
// 复用桌面端已登录的会话凭据，无需另申请密钥；纯 Go 标准库实现。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/server"
	"doubao2api/internal/store"
)

// version 由构建时注入：-ldflags "-X main.version=1.0.0"
var version = "dev"

func main() {
	var (
		host       = flag.String("host", envOr("DOUBAO_HOST", "127.0.0.1"), "监听地址")
		port       = flag.Int("port", envInt("DOUBAO_PORT", 10086), "监听端口")
		dataPath   = flag.String("data", envOr("DOUBAO_DATA_PATH", ""), "状态文件路径（默认在可执行文件同目录）")
		doubaoDir  = flag.String("doubao-dir", envOr("DOUBAO_DATA_DIR", ""), "DoubaoWork 数据目录（留空自动探测）")
		importOnly = flag.Bool("import", false, "仅执行一次桌面端导入后退出")
		noImport   = flag.Bool("no-import", false, "启动时不自动导入桌面端登录态")
		adminPw    = flag.String("admin-password", envOr("DOUBAO_ADMIN_PASSWORD", ""), "管理密码（非空时覆盖状态文件中的设置）")
		showVer    = flag.Bool("version", false, "打印版本后退出")
	)
	flag.Parse()

	if *showVer {
		fmt.Println("doubao2api", version)
		return
	}

	if *dataPath == "" {
		*dataPath = defaultDataPath()
	}

	st, err := store.Open(*dataPath)
	if err != nil {
		log.Fatalf("打开状态文件失败: %v", err)
	}
	log.Printf("状态文件: %s", st.Path())

	if *importOnly {
		runImport(st, *doubaoDir)
		return
	}

	// 命令行 / 环境变量优先级高于状态文件；设置后立即落盘。
	if *adminPw != "" {
		st.UpdateSettings(func(s *store.Settings) { s.AdminPassword = *adminPw })
		if err := st.Save(); err != nil {
			log.Printf("保存管理密码失败: %v", err)
		} else {
			log.Printf("已启用管理密码（来自命令行 / DOUBAO_ADMIN_PASSWORD）")
		}
	}

	if !*noImport {
		autoImport(st, *doubaoDir)
	}

	srv := server.New(st)
	// 积分自动签到 / 自动领取（默认关闭，控制台设置开启）。
	autoCtx, stopAuto := context.WithCancel(context.Background())
	defer stopAuto()
	srv.StartAutoRewards(autoCtx)
	// 待确认签到的后台复查（默认关闭，开启 retry_pending_checkin 后每小时一次）。
	srv.StartPendingCheckinRetry(autoCtx)

	addr := net.JoinHostPort(*host, fmt.Sprintf("%d", *port))
	// 启动时就把「对外监听 + 无密钥」这类组合点名，避免误暴露后才被发现。
	if !st.HasKeys() && !st.Settings().AllowAnonymous && !isLoopbackBind(*host) {
		log.Printf("警告: 未签发 API 密钥却监听在非本机地址 %s，/v1/* 将拒绝所有非本机请求；"+
			"请先在控制台创建密钥，或在设置中开启 allow_anonymous", *host)
	}
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 20 * time.Second,
		// 流式响应可能持续数分钟，不设 WriteTimeout
	}

	go func() {
		log.Printf("Doubao2API %s 已启动: http://%s/", version, addr)
		logSecurityMode(st)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("监听失败: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Printf("正在关闭…")
	stopAuto()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		log.Printf("关闭异常: %v", err)
	}
	if err := st.Save(); err != nil {
		log.Printf("保存状态失败: %v", err)
	}
	log.Printf("已退出")
}

// logSecurityMode 在启动时打印密钥 / 管理接口的实际生效状态。
func logSecurityMode(st *store.Store) {
	switch {
	case st.HasKeys():
		log.Printf("已签发 API 密钥，调用需携带 Authorization: Bearer sk-...")
	case st.Settings().AllowAnonymous:
		log.Printf("尚未签发 API 密钥，已开启匿名调用（allow_anonymous）")
	default:
		log.Printf("尚未签发 API 密钥，当前仅允许本机无密钥调用（控制台可创建）")
	}
	if st.Settings().AdminPassword != "" {
		log.Printf("管理接口已启用密码校验（X-Admin-Password / Bearer）")
	} else {
		log.Printf("未设置管理密码，管理接口仅允许本机访问（可用 -admin-password 设置）")
	}
}

// autoImport 在启动时尝试导入本机桌面端登录态（全部 profile）。
func autoImport(st *store.Store, dir string) {
	results, err := doubao.ImportAllFromDesktop(dir)
	if err != nil {
		log.Printf("未自动导入桌面端登录态（可在控制台手动导入）: %v", err)
		return
	}
	added, updated := 0, 0
	for _, res := range results {
		if st.UpsertAccount(res.Account) {
			added++
		} else {
			updated++
		}
	}
	if err := st.Save(); err != nil {
		log.Printf("保存账号失败: %v", err)
		return
	}
	for _, res := range results {
		log.Printf("已导入账号 %s（profile=%s，Cookie %d 项）", res.Account.Name, res.Profile, res.Cookies)
	}
	log.Printf("桌面端登录态导入完成：新增 %d，更新 %d", added, updated)
}

func runImport(st *store.Store, dir string) {
	results, err := doubao.ImportAllFromDesktop(dir)
	if err != nil {
		log.Fatalf("导入失败: %v", err)
	}
	added, updated := 0, 0
	for _, res := range results {
		isNew := st.UpsertAccount(res.Account)
		state := "更新"
		if isNew {
			state = "新增"
			added++
		} else {
			updated++
		}
		fmt.Printf("%s账号 %s\n数据目录: %s\nprofile: %s\nCookie: %d 项\n会话: %s\n\n",
			state, res.Account.Name, res.Dir, res.Profile, res.Cookies, res.Account.SessionID())
	}
	if err := st.Save(); err != nil {
		log.Fatalf("保存失败: %v", err)
	}
	fmt.Printf("共导入 %d 个账号（新增 %d，更新 %d）→ %s\n", len(results), added, updated, st.Path())
}

// defaultDataPath 返回默认状态文件路径（可执行文件同目录）。
func defaultDataPath() string {
	exe, err := os.Executable()
	if err != nil {
		return "doubao2api-data.json"
	}
	return filepath.Join(filepath.Dir(exe), "doubao2api-data.json")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// isLoopbackBind 判断监听地址是否仅限本机。
//
// 空串、localhost、环回 IP 视为仅本机；0.0.0.0 / :: / 具体网卡地址返回 false。
func isLoopbackBind(host string) bool {
	h := strings.TrimSpace(strings.ToLower(host))
	switch h {
	case "", "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	if ip := net.ParseIP(strings.Trim(h, "[]")); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}
