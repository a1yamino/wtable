package server

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"wtable/internal/model"
)

func validRoleName(name string) bool {
	return strings.TrimSpace(name) != "" && validMetadata(name, 80) && name != "." && name != ".." && !strings.ContainsAny(name, "/\\")
}

func validateRole(role model.RoleTemplate) error {
	if !validRoleName(role.Name) {
		return errors.New("角色名称须为 1–80 字节，不含路径分隔符或控制字符")
	}
	if !utf8.ValidString(role.Instructions) || strings.ContainsRune(role.Instructions, 0) || len(role.Instructions) > 16384 {
		return errors.New("角色指令须为有效 UTF-8，最多 16 KiB 且不含 NUL；启动 brief 总长仍限 2 KiB")
	}
	if !validMetadata(role.Preset, 80) || (role.Backend != "" && role.Backend != "pty" && role.Backend != "acp") || !validMetadata(role.Cwd, 4096) {
		return errors.New("角色默认 preset/backend/cwd 无效")
	}
	return nil
}

// Role files contain a small front matter block followed by the Markdown
// instructions. Quoted metadata round-trips paths and names without YAML deps.
func parseRole(data []byte, name string) (model.RoleTemplate, error) {
	role := model.RoleTemplate{Name: name}
	text := strings.ReplaceAll(string(data), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return role, errors.New("角色文件需要 --- 元数据头及 Markdown 指令正文")
	}
	end := strings.Index(text[4:], "\n---\n")
	if end < 0 {
		return role, errors.New("角色文件缺少元数据结束标记 ---")
	}
	header := text[4 : 4+end]
	seen := map[string]bool{}
	for _, line := range strings.Split(header, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		key, value, ok := strings.Cut(line, ":")
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if !ok || seen[key] {
			return role, errors.New("角色元数据格式无效或字段重复")
		}
		seen[key] = true
		if strings.HasPrefix(value, "\"") {
			var err error
			value, err = strconv.Unquote(value)
			if err != nil {
				return role, errors.New("角色元数据引号无效")
			}
		}
		switch key {
		case "name":
			if value != name {
				return role, errors.New("角色 name 必须与文件名一致")
			}
			role.Name = value
		case "preset":
			role.Preset = value
		case "backend":
			role.Backend = value
		case "cwd":
			role.Cwd = value
		default:
			return role, fmt.Errorf("角色元数据字段无效：%s", key)
		}
	}
	role.Instructions = strings.TrimSuffix(text[4+end+5:], "\n")
	return role, validateRole(role)
}

func (s *Server) roles() ([]model.RoleTemplate, error) {
	entries, err := os.ReadDir(filepath.Join(s.paths.Home, "roles"))
	if os.IsNotExist(err) {
		return []model.RoleTemplate{}, nil
	}
	if err != nil {
		return nil, err
	}
	roles := []model.RoleTemplate{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".md")
		role, err := s.role(name)
		if err != nil {
			return nil, fmt.Errorf("角色 %s：%w", name, err)
		}
		roles = append(roles, role)
	}
	sort.Slice(roles, func(i, j int) bool { return roles[i].Name < roles[j].Name })
	return roles, nil
}

func (s *Server) role(name string) (model.RoleTemplate, error) {
	if !validRoleName(name) {
		return model.RoleTemplate{}, errors.New("角色名称无效")
	}
	path := filepath.Join(s.paths.Home, "roles", name+".md")
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return model.RoleTemplate{}, errors.New("角色模板不存在")
	}
	if err != nil {
		return model.RoleTemplate{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > 32768 {
		return model.RoleTemplate{}, errors.New("角色文件须为不超过 32 KiB 的普通文件")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return model.RoleTemplate{}, err
	}
	return parseRole(data, name)
}

func (s *Server) putRole(role model.RoleTemplate) (model.RoleTemplate, error) {
	if err := validateRole(role); err != nil {
		return model.RoleTemplate{}, err
	}
	if role.Backend == "acp" {
		return model.RoleTemplate{}, errors.New(acpDisabledError)
	}
	if role.Preset != "" {
		presets, err := s.configuredPresets()
		if err != nil {
			return model.RoleTemplate{}, err
		}
		for _, preset := range presets {
			if preset.ID == role.Preset && preset.Backend == "acp" {
				return model.RoleTemplate{}, errors.New(acpDisabledError)
			}
		}
	}
	dir := filepath.Join(s.paths.Home, "roles")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return model.RoleTemplate{}, err
	}
	text := "---\nname: " + strconv.Quote(role.Name) + "\n"
	for _, field := range [][2]string{{"preset", role.Preset}, {"backend", role.Backend}, {"cwd", role.Cwd}} {
		if field[1] != "" {
			text += field[0] + ": " + strconv.Quote(field[1]) + "\n"
		}
	}
	text += "---\n" + role.Instructions + "\n"
	f, err := os.CreateTemp(dir, ".role-")
	if err != nil {
		return model.RoleTemplate{}, err
	}
	defer os.Remove(f.Name())
	if _, err = f.WriteString(text); err != nil {
		f.Close()
		return model.RoleTemplate{}, err
	}
	if err = f.Close(); err != nil {
		return model.RoleTemplate{}, err
	}
	if err = os.Rename(f.Name(), filepath.Join(dir, role.Name+".md")); err != nil {
		return model.RoleTemplate{}, err
	}
	return role, nil
}

func (s *Server) deleteRole(name string) error {
	if !validRoleName(name) {
		return errors.New("角色名称无效")
	}
	err := os.Remove(filepath.Join(s.paths.Home, "roles", name+".md"))
	if os.IsNotExist(err) {
		return errors.New("角色模板不存在")
	}
	return err
}

func roleHash(instructions string) string {
	sum := sha256.Sum256([]byte(instructions))
	return hex.EncodeToString(sum[:])
}

func (s *Server) resolveRole(args *model.SpawnParams) (model.RoleTemplate, error) {
	if args.RoleTemplate == "" {
		return model.RoleTemplate{}, nil
	}
	role, err := s.role(args.RoleTemplate)
	if err != nil {
		return role, err
	}
	if args.Role == "" {
		args.Role = role.Name
	}
	explicitPreset := args.Preset != ""
	if args.Preset == "" && len(args.Command) == 0 {
		args.Preset = role.Preset
	}
	if args.Backend == "" && !explicitPreset {
		args.Backend = role.Backend
	}
	if args.Cwd == "" && args.Layer == "" {
		args.Cwd = role.Cwd
	}
	return role, nil
}
