package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type keychainReference struct {
	Service string `json:"service"`
	Account string `json:"account,omitempty"`
}

type credentialSource struct {
	Literal     *string            `json:"literal,omitempty"`
	EnvRef      string             `json:"env_ref,omitempty"`
	KeychainRef *keychainReference `json:"keychain_ref,omitempty"`
}

type accountProfile struct {
	Name     string                      `json:"name"`
	Endpoint string                      `json:"endpoint,omitempty"`
	Env      map[string]string           `json:"env,omitempty"`
	Sources  map[string]credentialSource `json:"-"`
}

// Preserve the original string-valued env format, while allowing explicit
// references in the same map. No resolved credential is written back to disk.
func (p *accountProfile) UnmarshalJSON(data []byte) error {
	var raw struct {
		Name     string                     `json:"name"`
		Endpoint string                     `json:"endpoint"`
		Env      map[string]json.RawMessage `json:"env"`
	}
	if json.Unmarshal(data, &raw) != nil {
		return errors.New("accounts.json：账号配置格式无效")
	}
	*p = accountProfile{Name: raw.Name, Endpoint: raw.Endpoint, Env: map[string]string{}, Sources: map[string]credentialSource{}}
	for key, value := range raw.Env {
		var literal string
		if len(value) > 0 && value[0] == '"' && json.Unmarshal(value, &literal) == nil {
			p.Env[key] = literal
			continue
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(value, &fields) != nil || len(fields) != 1 {
			return errors.New("accounts.json：凭据值须为字符串或单一 literal/env_ref/keychain_ref 引用")
		}
		var source credentialSource
		if json.Unmarshal(value, &source) != nil {
			return errors.New("accounts.json：凭据引用格式无效")
		}
		count := 0
		if source.Literal != nil {
			count++
		}
		if source.EnvRef != "" {
			count++
		}
		if source.KeychainRef != nil {
			count++
		}
		if count != 1 {
			return errors.New("accounts.json：凭据值须指定一个非空 literal/env_ref/keychain_ref 来源")
		}
		p.Sources[key] = source
	}
	return nil
}

func validEnvKey(key string) bool {
	return key != "" && !strings.ContainsAny(key, "=\x00") && !strings.HasPrefix(key, "WTABLE_")
}

func (s *Server) accountProfiles() ([]accountProfile, error) {
	profiles := []accountProfile{}
	if err := readOnboardingConfig(filepath.Join(s.paths.Home, "accounts.json"), &profiles); err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for i := range profiles {
		p := &profiles[i]
		if strings.TrimSpace(p.Name) == "" || !validMetadata(p.Name, 80) || seen[p.Name] {
			return nil, errors.New("accounts.json：名称必须唯一且为 1–80 字节，不含控制字符")
		}
		seen[p.Name] = true
		if p.Endpoint == "" {
			p.Endpoint = "official"
		}
		if !contains([]string{"official", "compatible", "custom"}, p.Endpoint) {
			return nil, errors.New("accounts.json：endpoint 必须为 official/compatible/custom")
		}
		for key, value := range p.Env {
			if !validEnvKey(key) || strings.ContainsRune(value, 0) {
				return nil, errors.New("accounts.json：环境变量无效或试图覆盖 WTABLE_* 保留变量")
			}
		}
		for key, source := range p.Sources {
			if !validEnvKey(key) || (source.Literal != nil && strings.ContainsRune(*source.Literal, 0)) || (source.EnvRef != "" && !validEnvKey(source.EnvRef)) {
				return nil, errors.New("accounts.json：环境变量或凭据引用无效，不能覆盖或引用 WTABLE_* 保留变量")
			}
			if ref := source.KeychainRef; ref != nil && (strings.TrimSpace(ref.Service) == "" || !validMetadata(ref.Service, 256) || !validMetadata(ref.Account, 256)) {
				return nil, errors.New("accounts.json：keychain_ref 需要有效的 service，可选 account")
			}
		}
	}
	return profiles, nil
}

func (s *Server) accountEnv(name string) (map[string]string, error) {
	if name == "" {
		return nil, nil
	}
	profiles, err := s.accountProfiles()
	if err != nil {
		return nil, err
	}
	for _, p := range profiles {
		if p.Name != name {
			continue
		}
		env := map[string]string{}
		for key, value := range p.Env {
			env[key] = value
		}
		for key, source := range p.Sources {
			var value string
			switch {
			case source.Literal != nil:
				value = *source.Literal
			case source.EnvRef != "":
				var ok bool
				value, ok = os.LookupEnv(source.EnvRef)
				if !ok || value == "" {
					return nil, fmt.Errorf("账号 %s：环境变量 %s 的 env_ref 缺失或为空", p.Name, key)
				}
			case source.KeychainRef != nil:
				value, err = resolveKeychain(s.ctx, *source.KeychainRef)
				if err != nil {
					return nil, fmt.Errorf("账号 %s：环境变量 %s 的 keychain_ref 无法读取（钥匙串项目不存在、被锁定或未授权）", p.Name, key)
				}
			}
			if strings.ContainsRune(value, 0) {
				return nil, errors.New("accounts.json：解析后的环境变量含 NUL")
			}
			env[key] = value
		}
		return env, nil
	}
	return nil, errors.New("账号配置不存在；请检查 accounts.json")
}

func resolveKeychain(ctx context.Context, ref keychainReference) (string, error) {
	if runtime.GOOS != "darwin" {
		return "", errors.New("keychain_ref 仅支持 macOS")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	args := []string{"find-generic-password", "-s", ref.Service}
	if ref.Account != "" {
		args = append(args, "-a", ref.Account)
	}
	args = append(args, "-w")
	cmd := exec.CommandContext(ctx, "/usr/bin/security", args...)
	cmd.WaitDelay = 100 * time.Millisecond
	output := &boundedOutput{limit: 65536}
	cmd.Stdout = output
	if err := cmd.Run(); err != nil {
		return "", errors.New("keychain_ref 无法读取")
	}
	value := strings.TrimSuffix(output.String(), "\n")
	if value == "" {
		return "", errors.New("keychain_ref 为空")
	}
	return value, nil
}

func (s *Server) accountEndpoint(name string) (string, error) {
	if name == "" {
		return "official", nil
	}
	profiles, err := s.accountProfiles()
	if err != nil {
		return "", err
	}
	for _, profile := range profiles {
		if profile.Name == name {
			return profile.Endpoint, nil
		}
	}
	return "", errors.New("账号配置不存在；请检查 accounts.json")
}
