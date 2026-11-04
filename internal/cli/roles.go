package cli

import (
	"errors"
	"flag"
	"os"

	"wtable/internal/model"
)

func roleCommand(args []string, call func(string, any, any) error) error {
	if len(args) == 0 || (len(args) == 1 && args[0] == "list") {
		var roles []model.RoleTemplate
		if err := call("role.list", nil, &roles); err != nil {
			return err
		}
		return printJSON(roles)
	}
	if len(args) == 2 && args[0] == "delete" {
		return call("role.delete", map[string]string{"name": args[1]}, nil)
	}
	if args[0] == "put" {
		fs := flag.NewFlagSet("role put", flag.ContinueOnError)
		preset := fs.String("preset", "", "默认预设")
		backend := fs.String("backend", "", "默认后端（仅 pty）")
		cwd := fs.String("dir", "", "默认工作目录")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 2 {
			return errors.New("用法：wtable role put [--preset ID] [--backend pty] [--dir PATH] NAME INSTRUCTIONS.md")
		}
		text, err := os.ReadFile(fs.Arg(1))
		if err != nil {
			return err
		}
		role := model.RoleTemplate{Name: fs.Arg(0), Instructions: string(text), Preset: *preset, Backend: *backend, Cwd: *cwd}
		var result model.RoleTemplate
		if err = call("role.put", role, &result); err != nil {
			return err
		}
		return printJSON(result)
	}
	return errors.New("用法：wtable role list | put [OPTIONS] NAME FILE | delete NAME")
}
