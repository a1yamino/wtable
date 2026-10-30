package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"wtable/internal/model"
)

type boundedOutput struct {
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > b.limit {
		return 0, errors.New("Git 输出超过 2 MiB，请缩小查询范围")
	}
	return b.Buffer.Write(p)
}

func gitCommand(cwd string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	argv := append([]string{"--no-pager", "--no-optional-locks", "-C", cwd}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output := &boundedOutput{limit: 2 << 20}
	cmd.Stdout = output
	cmd.Stderr = output
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s：%w\n%s", args[0], err, short(output.String(), 2000))
	}
	return output.String(), nil
}

func (s *Server) createLayer(raw json.RawMessage) (model.Layer, error) {
	var a struct {
		Name string `json:"name"`
		Cwd  string `json:"cwd"`
		Base string `json:"base"`
	}
	if err := decode(raw, &a); err != nil {
		return model.Layer{}, err
	}
	if !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,39}$`).MatchString(a.Name) {
		return model.Layer{}, errors.New("layer 名称需要 1–40 个字母、数字、下划线或连字符")
	}
	if a.Cwd == "" {
		return model.Layer{}, errors.New("需要仓库目录")
	}
	repo, err := gitCommand(a.Cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return model.Layer{}, err
	}
	repo = strings.TrimSpace(repo)
	if a.Base == "" {
		a.Base = "origin/main"
		if _, err = gitCommand(repo, "rev-parse", "--verify", "refs/remotes/origin/main^{commit}"); err != nil {
			a.Base = "HEAD"
		}
	}
	base, err := gitCommand(repo, "rev-parse", "--verify", "--end-of-options", a.Base+"^{commit}")
	if err != nil {
		return model.Layer{}, err
	}
	layer := model.Layer{Name: a.Name, Repo: repo, Branch: "wtable/layer/" + a.Name, Path: filepath.Join(s.paths.Home, "worktrees", hash(repo)[:12], a.Name)}
	s.mu.Lock()
	for _, l := range s.state.Layers {
		if l.Repo == repo && l.Name == a.Name {
			s.mu.Unlock()
			return model.Layer{}, errors.New("layer 已存在")
		}
	}
	s.mu.Unlock()
	if err = os.MkdirAll(filepath.Dir(layer.Path), 0700); err != nil {
		return model.Layer{}, err
	}
	if _, err = gitCommand(repo, "worktree", "add", "-b", layer.Branch, layer.Path, strings.TrimSpace(base)); err != nil {
		return model.Layer{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Layers = append(s.state.Layers, layer)
	return layer, s.saveLocked()
}

func (s *Server) gitRead(method string, raw json.RawMessage) (any, error) {
	var a struct {
		Cwd  string `json:"cwd"`
		Path string `json:"path"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.Cwd == "" {
		return nil, errors.New("需要仓库目录")
	}
	if method == "git.changes" {
		return gitCommand(a.Cwd, "status", "--short")
	}
	if a.Path != "" {
		return gitCommand(a.Cwd, "diff", "--no-ext-diff", "--no-textconv", "--unified=3", "--", a.Path)
	}
	return gitCommand(a.Cwd, "diff", "--no-ext-diff", "--no-textconv", "--unified=3")
}
