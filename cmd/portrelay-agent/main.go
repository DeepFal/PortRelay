package main

import (
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/DeepFal/PortRelay/client"
)

//go:embed all:web
var webFS embed.FS

// version 由发布构建通过 -ldflags "-X main.version=..." 注入
var version = "dev"

func main() {
	httpPort := flag.Int("port", 33080, "Local dashboard HTTP port")
	noBrowser := flag.Bool("no-browser", false, "Do not auto-open browser")
	showVersion := flag.Bool("version", false, "Print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println("portrelay-agent", version)
		return
	}

	log.Printf("==================================================")
	log.Printf("  PortRelay Agent %s", version)
	log.Printf("  Dashboard: http://127.0.0.1:%d", *httpPort)
	log.Printf("==================================================")

	// 先占住面板端口：已有实例在运行时直接退出，避免第二个 Agent 连上服务端顶替会话
	addr := fmt.Sprintf("127.0.0.1:%d", *httpPort)
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		log.Fatalf("Dashboard port %s unavailable (is another PortRelay agent running?): %v", addr, err)
	}

	cm, err := client.NewConfigManager()
	if err != nil {
		log.Fatalf("Failed to init config manager: %v", err)
	}

	agent := client.NewAgent(cm)
	agent.Start()

	// Strip 'web' prefix for embed fs
	subFS, err := fs.Sub(webFS, "web")
	if err != nil {
		log.Fatalf("Failed to load embedded web assets: %v", err)
	}

	apiServer := client.NewAPIServer(cm, agent, http.FS(subFS))
	apiServer.OnShutdown = func() {
		log.Printf("Shutdown requested via API, stopping PortRelay Agent...")
		agent.Stop()
		_ = cm.Close()
		os.Exit(0)
	}

	// Graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		log.Printf("\nShutting down PortRelay Agent...")
		agent.Stop()
		_ = cm.Close()
		os.Exit(0)
	}()

	if !*noBrowser {
		go openBrowser(fmt.Sprintf("http://127.0.0.1:%d", *httpPort))
	}

	log.Printf("Serving local web control panel at http://%s", addr)
	if err := http.Serve(ln, apiServer.Handler()); err != nil {
		agent.Stop()
		_ = cm.Close()
		log.Fatalf("HTTP server failed: %v", err)
	}
}

func openBrowser(url string) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", url)
	default:
		cmd = exec.Command("xdg-open", url)
	}
	_ = cmd.Start()
}
