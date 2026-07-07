package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"x-ui/util/common"
)

type CaddyConfig struct {
	Path      string `json:"path"`
	CaddyBin  string `json:"caddyBin"`
	Caddyfile string `json:"caddyfile"`
	Content   string `json:"content"`
}

type CaddyCommandResult struct {
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
}

type CaddyService struct {
	settingService SettingService
}

func (s *CaddyService) GetPath() (string, error) {
	path, err := s.settingService.GetCaddyPath()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(path), nil
}

func (s *CaddyService) SetPath(path string) error {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return common.NewError("Caddy 目录不能为空")
	}
	if err := s.checkDir(path); err != nil {
		return err
	}
	if err := s.checkCaddyBin(path); err != nil {
		return err
	}
	return s.settingService.SetCaddyPath(path)
}

func (s *CaddyService) GetConfig() (*CaddyConfig, error) {
	path, err := s.GetPath()
	if err != nil {
		return nil, err
	}
	if err := s.checkDir(path); err != nil {
		return nil, err
	}
	if err := s.checkCaddyBin(path); err != nil {
		return nil, err
	}

	caddyfile := s.caddyfile(path)
	content, err := os.ReadFile(caddyfile)
	if err != nil {
		return nil, err
	}
	return &CaddyConfig{
		Path:      path,
		CaddyBin:  s.caddyBin(path),
		Caddyfile: caddyfile,
		Content:   string(content),
	}, nil
}

func (s *CaddyService) Validate(content string) (*CaddyCommandResult, error) {
	path, err := s.GetPath()
	if err != nil {
		return nil, err
	}
	return s.validateWithPath(path, content)
}

func (s *CaddyService) Save(content string) (*CaddyConfig, error) {
	path, err := s.GetPath()
	if err != nil {
		return nil, err
	}
	if _, err := s.validateWithPath(path, content); err != nil {
		return nil, err
	}
	if _, err := s.writeCaddyfile(path, content); err != nil {
		return nil, err
	}
	return s.GetConfig()
}

func (s *CaddyService) Reload() (*CaddyCommandResult, error) {
	path, err := s.GetPath()
	if err != nil {
		return nil, err
	}
	return s.reloadWithPath(path)
}

func (s *CaddyService) SaveAndReload(content string) (*CaddyCommandResult, error) {
	path, err := s.GetPath()
	if err != nil {
		return nil, err
	}
	if _, err := s.validateWithPath(path, content); err != nil {
		return nil, err
	}

	backupPath, err := s.writeCaddyfile(path, content)
	if err != nil {
		return nil, err
	}

	result, err := s.reloadWithPath(path)
	if err == nil {
		return result, nil
	}

	rollbackErr := s.rollback(path, backupPath)
	if rollbackErr == nil {
		_, _ = s.reloadWithPath(path)
	}
	if rollbackErr != nil {
		return result, common.NewError("reload 失败，且回滚失败: ", err, "; rollback: ", rollbackErr)
	}
	return result, common.NewError("reload 失败，已恢复旧 Caddyfile: ", err)
}

func (s *CaddyService) caddyBin(path string) string {
	return filepath.Join(path, "caddy")
}

func (s *CaddyService) caddyfile(path string) string {
	return filepath.Join(path, "Caddyfile")
}

func (s *CaddyService) checkDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return common.NewError("Caddy 路径不是目录: ", path)
	}
	return nil
}

func (s *CaddyService) checkCaddyBin(path string) error {
	bin := s.caddyBin(path)
	info, err := os.Stat(bin)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return common.NewError("caddy 不是可执行文件: ", bin)
	}
	if info.Mode()&0111 == 0 {
		return common.NewError("caddy 文件没有执行权限: ", bin)
	}
	return nil
}

func (s *CaddyService) validateWithPath(path string, content string) (*CaddyCommandResult, error) {
	if err := s.checkDir(path); err != nil {
		return nil, err
	}
	if err := s.checkCaddyBin(path); err != nil {
		return nil, err
	}

	tmpFile, err := os.CreateTemp(path, ".Caddyfile.x-ui.validate.*")
	if err != nil {
		return nil, err
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return nil, err
	}
	if err := tmpFile.Close(); err != nil {
		return nil, err
	}

	return s.runCaddy(path, "validate", "--config", tmpPath, "--adapter", "caddyfile")
}

func (s *CaddyService) reloadWithPath(path string) (*CaddyCommandResult, error) {
	if err := s.checkDir(path); err != nil {
		return nil, err
	}
	if err := s.checkCaddyBin(path); err != nil {
		return nil, err
	}
	return s.runCaddy(path, "reload", "--config", s.caddyfile(path), "--adapter", "caddyfile")
}

func (s *CaddyService) runCaddy(path string, args ...string) (*CaddyCommandResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, s.caddyBin(path), args...)
	cmd.Dir = path

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	result := &CaddyCommandResult{
		Stdout: stdout.String(),
		Stderr: stderr.String(),
	}
	if ctx.Err() == context.DeadlineExceeded {
		return result, common.NewError("caddy 命令执行超时")
	}
	if err != nil {
		return result, common.NewError(err, "\n", result.Stderr)
	}
	return result, nil
}

func (s *CaddyService) writeCaddyfile(path string, content string) (string, error) {
	if err := s.checkDir(path); err != nil {
		return "", err
	}

	caddyfile := s.caddyfile(path)
	backupPath := ""
	mode := os.FileMode(0644)

	info, err := os.Stat(caddyfile)
	if err == nil {
		mode = info.Mode()
		backupPath = filepath.Join(path, fmt.Sprintf("Caddyfile.x-ui.bak.%s", time.Now().Format("20060102150405")))
		if err := copyFile(caddyfile, backupPath, mode); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}

	tmpFile, err := os.CreateTemp(path, ".Caddyfile.x-ui.tmp.*")
	if err != nil {
		return "", err
	}
	tmpPath := tmpFile.Name()
	ok := false
	defer func() {
		if !ok {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmpFile.WriteString(content); err != nil {
		tmpFile.Close()
		return "", err
	}
	if err := tmpFile.Close(); err != nil {
		return "", err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return "", err
	}
	if err := os.Rename(tmpPath, caddyfile); err != nil {
		return "", err
	}
	ok = true
	return backupPath, nil
}

func (s *CaddyService) rollback(path string, backupPath string) error {
	caddyfile := s.caddyfile(path)
	if backupPath == "" {
		return os.Remove(caddyfile)
	}
	info, err := os.Stat(backupPath)
	if err != nil {
		return err
	}
	return copyFile(backupPath, caddyfile, info.Mode())
}

func copyFile(src string, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
