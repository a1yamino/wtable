package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const Version = "0.3.0"

type Paths struct{ Home, Socket, Token, Database, Log string }

func Resolve() (Paths, error) {
	home := os.Getenv("WTABLE_HOME")
	if home == "" {
		base, err := os.UserConfigDir()
		if err != nil {
			return Paths{}, err
		}
		home = filepath.Join(base, "wtable")
	}
	home, err := filepath.Abs(home)
	if err != nil {
		return Paths{}, err
	}
	p := Paths{Home: home, Socket: filepath.Join(home, "daemon.sock"), Token: filepath.Join(home, "controller.token"), Database: filepath.Join(home, "state.db"), Log: filepath.Join(home, "daemon.log")}
	if len(p.Socket) >= 104 {
		return Paths{}, fmt.Errorf("WTABLE_HOME 路径过长，请使用更短的目录：%s", home)
	}
	return p, nil
}

func (p Paths) Prepare() error {
	if err := os.MkdirAll(p.Home, 0700); err != nil {
		return err
	}
	return os.Chmod(p.Home, 0700)
}

func Secret() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (p Paths) ControllerToken() (string, error) {
	b, err := os.ReadFile(p.Token)
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(string(b))
	if len(token) != 64 {
		return "", fmt.Errorf("控制端凭证损坏：%s", p.Token)
	}
	return token, nil
}

func (p Paths) EnsureToken() (string, error) {
	token, err := p.ControllerToken()
	if err == nil {
		return token, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	token, err = Secret()
	if err != nil {
		return "", err
	}
	f, err := os.OpenFile(p.Token, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", err
	}
	if _, err = f.WriteString(token + "\n"); err != nil {
		f.Close()
		return "", err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return "", err
	}
	return token, f.Close()
}

func (p Paths) ClientToken() (string, error) {
	if token := os.Getenv("WTABLE_PEER_TOKEN"); token != "" {
		return token, nil
	}
	return p.ControllerToken()
}
