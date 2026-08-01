package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuanshu-ai/yuanshu/internal/poc/relay"
)

type Server struct{ Listen, Cert, Key, NodeToken string }
type Node struct {
	ServerURL, NodeToken, Workspace string
	ArchiveOnClose                  bool
}

func ServerFromEnv() (Server, error) {
	c := Server{Listen: os.Getenv("YUANSHU_POC_LISTEN"), Cert: os.Getenv("YUANSHU_POC_TLS_CERT"), Key: os.Getenv("YUANSHU_POC_TLS_KEY"), NodeToken: os.Getenv("YUANSHU_POC_NODE_TOKEN")}
	if c.Listen == "" {
		c.Listen = "127.0.0.1:7443"
	}
	if err := relay.ValidateLoopbackListen(c.Listen); err != nil {
		return Server{}, err
	}
	if err := regularFile(c.Cert, "YUANSHU_POC_TLS_CERT"); err != nil {
		return Server{}, err
	}
	if err := regularFile(c.Key, "YUANSHU_POC_TLS_KEY"); err != nil {
		return Server{}, err
	}
	if len(c.NodeToken) < 32 {
		return Server{}, errors.New("YUANSHU_POC_NODE_TOKEN must contain at least 32 bytes")
	}
	return c, nil
}

func NodeFromEnv() (Node, error) {
	c, err := localNodeFromEnv()
	if err != nil {
		return Node{}, err
	}
	c.ServerURL = os.Getenv("YUANSHU_POC_SERVER_URL")
	if !strings.HasPrefix(c.ServerURL, "wss://") {
		return Node{}, errors.New("YUANSHU_POC_SERVER_URL must use wss://")
	}
	return c, nil
}

func localNodeFromEnv() (Node, error) {
	c := Node{NodeToken: os.Getenv("YUANSHU_POC_NODE_TOKEN"), Workspace: os.Getenv("YUANSHU_POC_WORKSPACE"), ArchiveOnClose: os.Getenv("YUANSHU_POC_ARCHIVE_ON_CLOSE") == "1"}
	if len(c.NodeToken) < 32 {
		return Node{}, errors.New("YUANSHU_POC_NODE_TOKEN must contain at least 32 bytes")
	}
	abs, err := workspace(c.Workspace)
	if err != nil {
		return Node{}, err
	}
	c.Workspace = abs
	return c, nil
}

func StandaloneFromEnv() (Server, Node, error) {
	s, err := ServerFromEnv()
	if err != nil {
		return Server{}, Node{}, err
	}
	n, err := localNodeFromEnv()
	if err != nil {
		return Server{}, Node{}, err
	}
	return s, n, nil
}
func regularFile(path, name string) error {
	if path == "" {
		return fmt.Errorf("%s is required", name)
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s must name a regular file", name)
	}
	return nil
}
func workspace(path string) (string, error) {
	if path == "" {
		return "", errors.New("YUANSHU_POC_WORKSPACE is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", errors.New("invalid PoC workspace")
	}
	info, err := os.Stat(abs)
	if err != nil || !info.IsDir() {
		return "", errors.New("YUANSHU_POC_WORKSPACE must be an existing directory")
	}
	if filepath.Dir(abs) == abs {
		return "", errors.New("filesystem root cannot be a PoC workspace")
	}
	return filepath.Clean(abs), nil
}
