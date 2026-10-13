package selfupdate

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// PrepareContainerRuntime 由镜像内的启动脚本调用。程序与备份落在数据卷，
// 配置和业务数据仍使用固定 /app 根目录，不随可执行文件位置迁移。
// 不访问 Docker socket，也不修改宿主机或其他容器。
func PrepareContainerRuntime(root, image, imageVersion string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", errors.New("容器更新目录必须为绝对路径")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	p := pathsForExecutable(filepath.Join(root, "artex"))
	if filepath.Clean(image) == p.Current {
		return "", errors.New("镜像程序与更新目录不能相同")
	}
	// 新版本根本无法执行时，在启动器中恢复旧版，不依赖坏程序执行 Bootstrap。
	if _, pending := readMarker(p.Marker); pending {
		// current→old 与 new→current 两次 rename 之间被终止时，先恢复入口。
		// 保留暂存件和校验和，恢复后的程序仍走正常校验和换装。
		if _, err := os.Stat(p.Current); errors.Is(err, os.ErrNotExist) {
			if err := os.Rename(p.Old, p.Current); err != nil {
				return "", fmt.Errorf("恢复中断换装的入口: %w", err)
			}
		}
		if _, err := os.Stat(p.New); errors.Is(err, os.ErrNotExist) {
			if err := smokeTest(p.Current); err != nil {
				m, _ := readMarker(p.Marker)
				if err := rollback(p); err != nil {
					return "", fmt.Errorf("新版本无法执行且回滚失败: %w", err)
				}
				if err := saveContainerNotice(root, State{RolledBack: true, Detail: "新版本无法执行，已回滚到 " + orUnknown(m.From)}); err != nil {
					return "", err
				}
				if err := os.Remove(p.Marker); err != nil {
					return "", err
				}
			}
		}
		return p.Current, nil
	}
	_, currentErr := os.Stat(p.Current)
	if currentErr != nil && !errors.Is(currentErr, os.ErrNotExist) {
		return "", currentErr
	}
	lastImage := readVersion(filepath.Join(root, "image.version"))
	active := readVersion(p.Current + ".version")
	newerImage, comparable := CompareVersions(imageVersion, active)
	_, formalImage := CompareVersions(imageVersion, imageVersion)
	newImageIdentity := lastImage != imageVersion
	if errors.Is(currentErr, os.ErrNotExist) || (newImageIdentity && formalImage && (!comparable || newerImage > 0)) {
		stage := p.New + ".image"
		if err := copyProgram(image, stage); err != nil {
			return "", err
		}
		defer os.Remove(stage)
		if currentErr == nil {
			// 复用换装协议，镜像切换同样保留上一版和启动失败保护。
			if err := writeMarker(p.Marker, marker{From: active, To: imageVersion, Preparing: true}); err != nil {
				return "", err
			}
			sum, err := fileSHA256(stage)
			if err != nil {
				return "", err
			}
			if err := atomicText(p.Sum, sum); err != nil {
				return "", err
			}
			if err := os.Rename(stage, p.New); err != nil {
				return "", err
			}
			if err := swap(p); err != nil {
				return "", err
			}
		} else {
			if err := os.Rename(stage, p.Current); err != nil {
				return "", err
			}
			if err := atomicText(p.Current+".version", imageVersion); err != nil {
				return "", err
			}
		}
		_ = os.Remove(filepath.Join(root, "notice.json"))
	}
	// 记录已见过的镜像身份，避免同一镜像覆盖用户主动回滚的版本。
	if err := atomicText(filepath.Join(root, "image.version"), imageVersion); err != nil {
		return "", err
	}
	return p.Current, nil
}

func copyProgram(from, to string) error {
	src, err := os.Open(from)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(dst, src)
	syncErr := dst.Sync()
	closeErr := dst.Close()
	return errors.Join(copyErr, syncErr, closeErr)
}

func readVersion(path string) string { b, _ := os.ReadFile(path); return strings.TrimSpace(string(b)) }

func atomicText(path, value string) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write([]byte(value))
	err = errors.Join(writeErr, f.Sync(), f.Close())
	if err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func saveContainerNotice(root string, st State) error {
	// 仅保存故障说明，不包含业务配置或凭据。
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicText(filepath.Join(root, "notice.json"), string(b))
}

// Pending 从持久标记读取，容器/进程重启后仍能判断新版是否已稳定。
func Pending() bool {
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	_, ok := readMarker(p.Marker)
	return ok
}

// PersistentRuntime 报告实际程序是否位于启动器配置的持久更新目录。
// 热更新到新程序但仍使用旧镜像入口时不能声称已经启用持久化。
func PersistentRuntime() bool {
	root := os.Getenv("ARTEX_SELFUPDATE_ROOT")
	if !filepath.IsAbs(root) {
		return false
	}
	p, err := ResolvePaths()
	if err != nil {
		return false
	}
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	return filepath.Clean(root) == p.Dir
}
