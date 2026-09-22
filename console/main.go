package main

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type deploymentKeys struct {
	AdminKey  string `json:"admin_key"`
	APIKey    string `json:"api_key"`
	BridgeKey string `json:"bridge_key"`
}

func validateDeploymentKeys(keys deploymentKeys) error {
	if len(keys.AdminKey) < 32 || keys.APIKey == "" || len(keys.BridgeKey) < 32 || keys.AdminKey == keys.APIKey || keys.AdminKey == keys.BridgeKey || keys.APIKey == keys.BridgeKey {
		return errors.New("管理和桥接密钥须至少 32 字节，API Key 须非空，三种密钥须互不相同")
	}
	return nil
}

func readDeploymentKeys(path string, timeout time.Duration, adminOverride, apiOverride string) (deploymentKeys, error) {
	deadline := time.Now().Add(timeout)
	for {
		info, err := os.Lstat(path)
		if err == nil && (!info.Mode().IsRegular() || info.Size() > 4096) {
			return deploymentKeys{}, errors.New("部署密钥文件无效")
		}
		if err == nil {
			f, openErr := os.Open(path)
			if openErr != nil {
				return deploymentKeys{}, openErr
			}
			var keys deploymentKeys
			decoder := json.NewDecoder(io.LimitReader(f, 4097))
			decoder.DisallowUnknownFields()
			decodeErr := decoder.Decode(&keys)
			if decodeErr == nil {
				if trailing := decoder.Decode(new(any)); trailing != io.EOF {
					decodeErr = errors.New("部署密钥文件包含多余内容")
				}
			}
			closeErr := f.Close()
			if decodeErr != nil {
				return deploymentKeys{}, errors.New("部署密钥文件损坏")
			}
			if closeErr != nil {
				return deploymentKeys{}, closeErr
			}
			if err := validateDeploymentKeys(keys); err != nil {
				return deploymentKeys{}, err
			}
			if adminOverride != "" {
				keys.AdminKey = adminOverride
			}
			if apiOverride != "" {
				keys.APIKey = apiOverride
			}
			return keys, validateDeploymentKeys(keys)
		}
		if !errors.Is(err, os.ErrNotExist) {
			return deploymentKeys{}, err
		}
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return deploymentKeys{}, errors.New("等待部署密钥超时")
		}
		if remaining > 100*time.Millisecond {
			remaining = 100 * time.Millisecond
		}
		time.Sleep(remaining)
	}
}

func configFromEnv() (Config, string, error) {
	raw := os.Getenv("WB2A_CORE_URL")
	if raw == "" {
		raw = "http://core:7863"
	}
	target, err := url.Parse(raw)
	if err != nil {
		return Config{}, "", errors.New("WB2A_CORE_URL 无效")
	}
	listen := os.Getenv("WB2A_LISTEN")
	if listen == "" {
		listen = ":7863"
	}
	keyFile := os.Getenv("WB2A_KEY_FILE")
	if keyFile == "" {
		return Config{CoreURL: target, AdminKey: os.Getenv("WB2A_ADMIN_KEY"), APIKey: os.Getenv("WB2A_API_KEY"), BridgeKey: os.Getenv("WB2A_BRIDGE_KEY"), PublicOrigin: os.Getenv("WB2A_PUBLIC_ORIGIN"), TrustedProxyCIDRs: os.Getenv("WB2A_TRUSTED_PROXY_CIDRS"), RequireHTTPS: os.Getenv("WB2A_REQUIRE_HTTPS") == "1"}, listen, nil
	}
	keys, err := readDeploymentKeys(keyFile, 30*time.Second, os.Getenv("WB2A_ADMIN_KEY"), os.Getenv("WB2A_API_KEY"))
	if err != nil {
		return Config{}, "", err
	}
	return Config{CoreURL: target, AdminKey: keys.AdminKey, APIKey: keys.APIKey, BridgeKey: keys.BridgeKey, PublicOrigin: os.Getenv("WB2A_PUBLIC_ORIGIN"), TrustedProxyCIDRs: os.Getenv("WB2A_TRUSTED_PROXY_CIDRS"), RequireHTTPS: os.Getenv("WB2A_REQUIRE_HTTPS") == "1"}, listen, nil
}
func httpsExposure(listen, publicOrigin string, requireHTTPS bool) (string, error) {
	host := listen
	if split, _, err := net.SplitHostPort(listen); err == nil {
		host = split
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return "", nil
	}
	if strings.HasPrefix(publicOrigin, "https://") {
		return "", nil
	}
	const plain = "console 只在明文 HTTP 上监听，且监听地址不是回环，管理密钥与会话 Cookie 会随请求明文传输"
	if requireHTTPS {
		return plain, errors.New(plain + "；WB2A_REQUIRE_HTTPS 已开启，拒绝启动。请改为回环监听或把 WB2A_PUBLIC_ORIGIN 设为 https://你的域名")
	}
	return plain + "。公网暴露前请在反向代理终止 TLS 并把 WB2A_PUBLIC_ORIGIN 设为 https://你的域名", nil
}
func main() {
	cfg, listen, err := configFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	h, err := NewServer(cfg)
	if err != nil {
		log.Fatal(err)
	}
	warning, err := httpsExposure(listen, cfg.PublicOrigin, cfg.RequireHTTPS)
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[console] 管理密钥（仅交给管理员）: %s", cfg.AdminKey)
	server := &http.Server{Addr: listen, Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 60 * time.Second}
	log.Printf("console listening on %s", listen)
	if warning != "" {
		log.Printf("[console] 警告：%s", warning)
	}
	log.Fatal(server.ListenAndServe())
}
