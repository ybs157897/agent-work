package main

import (
	"log"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/application"
)

func configureKnowledgeAccess(svc *application.Service, listenAddress, workbenchRoot string) {
	endpoint := strings.TrimSpace(os.Getenv("ATW_KNOWLEDGE_ENDPOINT"))
	if endpoint == "" {
		endpoint = localKnowledgeEndpoint(listenAddress)
	}
	client := resolveKnowledgeClient(workbenchRoot)
	if endpoint == "" {
		log.Printf("knowledge: Agent 调用入口未就绪，需要可达监听地址")
		return
	}
	svc.KnowledgeEndpoint = endpoint
	svc.KnowledgeCLIPath = client
	if client == "" {
		log.Printf("knowledge: Codex 内置知识工具已配置；其他运行时需要构建 atw-knowledge 客户端")
	} else {
		log.Printf("knowledge: 本机 Agent 查询和记录入口已配置")
	}
}

func localKnowledgeEndpoint(listenAddress string) string {
	host, port, err := net.SplitHostPort(listenAddress)
	if err != nil {
		return ""
	}
	n, err := strconv.Atoi(port)
	if err != nil || n <= 0 || n > 65535 {
		return ""
	}
	switch host {
	case "", "0.0.0.0":
		host = "127.0.0.1"
	case "::":
		host = "::1"
	}
	return (&url.URL{Scheme: "http", Host: net.JoinHostPort(host, port)}).String()
}

func resolveKnowledgeClient(workbenchRoot string) string {
	checked := func(path string) string {
		if path == "" {
			return ""
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workbenchRoot, path)
		}
		info, err := os.Stat(path)
		if err != nil || info.IsDir() || info.Mode().Perm()&0o111 == 0 {
			return ""
		}
		return filepath.Clean(path)
	}
	if explicit := strings.TrimSpace(os.Getenv("ATW_KNOWLEDGE_CLIENT")); explicit != "" {
		return checked(explicit)
	}
	if executable, err := os.Executable(); err == nil {
		if client := checked(filepath.Join(filepath.Dir(executable), "atw-knowledge")); client != "" {
			return client
		}
	}
	if client := checked(filepath.Join(workbenchRoot, "bin", "atw-knowledge")); client != "" {
		return client
	}
	if candidate, err := exec.LookPath("atw-knowledge"); err == nil {
		return checked(candidate)
	}
	return ""
}
