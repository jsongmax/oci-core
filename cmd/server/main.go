// Command server 是 OCI Core 的单二进制服务：HTTP 接口 + 嵌入式前端。
package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"ocicore/internal/config"
	"ocicore/internal/cryptobox"
	"ocicore/internal/httpapi"
	"ocicore/internal/instancesvc"
	"ocicore/internal/ociconn"
	"ocicore/internal/store"
	"ocicore/internal/web"
)

// version 由构建时通过 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	if err := run(); err != nil {
		slog.Error("启动失败", "err", err)
		os.Exit(1)
	}
}

func run() error {
	httpapi.Version = version

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	// 主密钥优先取环境变量，便于容器化部署时由外部密钥管理注入；
	// 未提供则回落到数据目录下的密钥文件，首次运行时自动生成。
	var masterKey []byte
	if cfg.MasterKeyHex != "" {
		if masterKey, err = cryptobox.DecodeKey(cfg.MasterKeyHex); err != nil {
			return err
		}
		slog.Info("主密钥来自环境变量", "var", "OCICORE_MASTER_KEY（或兼容的 OCI_TOOLS_MASTER_KEY）")
	} else {
		if masterKey, err = cryptobox.LoadOrCreateMasterKey(cfg.MasterKeyPath()); err != nil {
			return err
		}
		slog.Info("主密钥文件就绪", "path", cfg.MasterKeyPath())
	}

	box, err := cryptobox.New(masterKey)
	if err != nil {
		return err
	}

	st, err := store.Open(cfg.DBPath(), box)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := warnOnFirstRun(st, cfg); err != nil {
		return err
	}

	conns := ociconn.New(st)
	instances := instancesvc.New(st, conns, instancesvc.NewBus())

	api := httpapi.New(httpapi.Deps{
		Store:     st,
		Config:    cfg,
		Conns:     conns,
		Instances: instances,
	})
	handler, err := buildHandler(api, cfg)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go purgeSessionsPeriodically(ctx, st)

	// 定期复查凭据。间隔从设置里读，改动一分钟内生效，不需要重启。
	go api.RunAccountChecker(ctx)
	go api.RunAuditPruner(ctx)
	// 容量守候。调度器只看持久化的 next_at，重启后退避进度原样恢复——
	// 只存内存的话每次重启都把退避清零，等于变相提高了对 Oracle 的请求频率。
	go api.RunHunter(ctx)
	go api.RunCapacityMonitor(ctx)

	// 进程重启会丢掉内存里的轮询协程。不恢复的话，那些停在"关机中"的行
	// 要等到下一轮全量同步才会更新，用户会以为界面卡死了。
	instances.ResumeWatches(ctx)

	settings, err := st.Settings(ctx)
	if err != nil {
		return err
	}
	instances.StartBackgroundSync(ctx, time.Duration(settings.SyncIntervalMinutes)*time.Minute)

	errCh := make(chan error, 1)
	go func() {
		slog.Info("服务已启动", "addr", cfg.Addr, "version", version, "data", cfg.DataDir)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		slog.Info("收到退出信号，正在关闭")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}

// warnOnFirstRun 在首次运行与公网监听时给出明确提示。
//
// 这个面板持有全部 Oracle 租户的完整控制权，暴露在公网上等同于把
// 所有云账号的钥匙挂在门口。默认只听回环，用户改成公网时必须知情。
func warnOnFirstRun(st *store.Store, cfg config.Config) error {
	count, err := st.CountUsers(context.Background())
	if err != nil {
		return err
	}
	if count == 0 {
		slog.Info("尚未初始化，请在浏览器中打开面板完成首次设置")
	}
	if cfg.ListensPublicly() {
		slog.Warn("正在监听非回环地址，本面板持有全部 Oracle 租户的控制权，" +
			"请务必置于 TLS 反向代理之后，并确认已启用两步验证")
	}
	return nil
}

// buildHandler 把 API 与前端资源组合成一个处理器。
//
// 配置了 StaticDir 就用磁盘上的目录，否则用编译时嵌入的产物。
// 前者是给前端开发用的：改完 vite build 一下就能看到效果，不用重编 Go。
func buildHandler(api *httpapi.Server, cfg config.Config) (http.Handler, error) {
	var assets fs.FS
	if cfg.StaticDir != "" {
		slog.Info("使用磁盘上的前端资源", "dir", cfg.StaticDir)
		assets = os.DirFS(cfg.StaticDir)
	} else {
		embedded, err := web.FS()
		if err != nil {
			return nil, err
		}
		assets = embedded
	}
	fileServer := http.FileServer(http.FS(assets))

	mux := http.NewServeMux()
	mux.Handle("/api/", api.Handler())
	mux.Handle("/", spaHandler(assets, fileServer))
	return mux, nil
}

// spaHandler 提供静态资源，并把未匹配的路径回落到 index.html，
// 让前端路由（/accounts、/instances 等）在直接访问时也能正常渲染。
func spaHandler(assets fs.FS, fileServer http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(assets, path); err == nil {
			fileServer.ServeHTTP(w, r)
			return
		}
		// 未知路径一律交给 SPA 处理。带扩展名的请求例外——
		// 那是真的找不到资源，回落 HTML 只会让调试更困难。
		if strings.Contains(pathBase(path), ".") {
			http.NotFound(w, r)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = "/"
		fileServer.ServeHTTP(w, r2)
	})
}

func pathBase(p string) string {
	if idx := strings.LastIndex(p, "/"); idx >= 0 {
		return p[idx+1:]
	}
	return p
}

// purgeSessionsPeriodically 定期清理过期会话，避免表无限增长。
func purgeSessionsPeriodically(ctx context.Context, st *store.Store) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if n, err := st.PurgeExpiredSessions(ctx); err != nil {
				slog.Warn("清理过期会话失败", "err", err)
			} else if n > 0 {
				slog.Info("已清理过期会话", "count", n)
			}
		}
	}
}
