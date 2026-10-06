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
	"syscall"
	"time"

	"doubao2api/internal/doubao"
	"doubao2api/internal/server"
	"doubao2api/internal/store"
)

func main() {
	var (
		host       = flag.String("host", envOr("DOUBAO_HOST", "127.0.0.1"), "监听地址")
		port       = flag.Int("port", envInt("DOUBAO_PORT", 10086), "监听端口")
		dataPath   = flag.String("data", envOr("DOUBAO_DATA_PATH", ""), "状态文件路径（默认在可执行文件同目录）")
		doubaoDir  = flag.String("doubao-dir", envOr("DOUBAO_DATA_DIR", ""), "DoubaoWork 数据目录（留空自动探测）")
		importOnly = flag.Bool("import", false, "仅执行一次桌面端导入后退出")
		noImport   = flag.Bool("no-import", false, "启动时不自动导入桌面端登录态")
	)
	flag.Parse()

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

	if !*noImport {
		autoImport(st, *doubaoDir)
	}

	srv := server.New(st)

	addr := net.JoinHostPort(*host, fmt.Sprintf("%d", *port))
	httpSrv := &http.Server{
		Addr:              addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 20 * time.Second,
		// 流式响应可能持续数分钟，不设 WriteTimeout
	}

	go func() {
		log.Printf("Doubao2API 已启动: http://%s/", addr)
		if st.HasKeys() {
			log.Printf("已签发 API 密钥，调用需携带 Authorization: Bearer sk-...")
		} else {
			log.Printf("尚未签发 API 密钥，当前允许无密钥调用（控制台可创建）")
		}
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("监听失败: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Printf("正在关闭…")
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

// autoImport 在启动时尝试导入本机桌面端登录态。
func autoImport(st *store.Store, dir string) {
	res, err := doubao.ImportFromDesktop(dir)
	if err != nil {
		log.Printf("未自动导入桌面端登录态（可在控制台手动导入）: %v", err)
		return
	}
	isNew := st.UpsertAccount(res.Account)
	if err := st.Save(); err != nil {
		log.Printf("保存账号失败: %v", err)
		return
	}
	verb := "已导入"
	if !isNew {
		verb = "已更新"
	}
	log.Printf("%s账号 %s（profile=%s，Cookie %d 项）", verb, res.Account.Name, res.Profile, res.Cookies)
}

func runImport(st *store.Store, dir string) {
	res, err := doubao.ImportFromDesktop(dir)
	if err != nil {
		log.Fatalf("导入失败: %v", err)
	}
	isNew := st.UpsertAccount(res.Account)
	if err := st.Save(); err != nil {
		log.Fatalf("保存失败: %v", err)
	}
	state := "更新"
	if isNew {
		state = "新增"
	}
	fmt.Printf("%s账号 %s\n数据目录: %s\nprofile: %s\nCookie: %d 项\n会话: %s\n",
		state, res.Account.Name, res.Dir, res.Profile, res.Cookies, res.Account.SessionID())
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

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		var n int
		if _, err := fmt.Sscanf(v, "%d", &n); err == nil {
			return n
		}
	}
	return def
}
