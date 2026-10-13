package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestContainerRuntimeSurvivesRecreation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "persistent")
	image := filepath.Join(t.TempDir(), "artex")
	os.WriteFile(image, []byte("image-1"), 0755)
	current, err := PrepareContainerRuntime(root, image, "2.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if current == image {
		t.Fatal("程序必须运行在持久卷")
	}
	os.WriteFile(current, []byte("online-2"), 0755)
	os.WriteFile(current+".version", []byte("2.2.4"), 0600)
	os.WriteFile(image, []byte("recreated-image-1"), 0755)
	got, err := PrepareContainerRuntime(root, image, "2.2.3")
	if err != nil || got != current || readAll(t, got) != "online-2" {
		t.Fatalf("重建丢失更新: %s %v", got, err)
	}
	// 更新镜像确实更高时才切换；保留上一版，随后重建不会覆盖主动回滚。
	os.WriteFile(image, []byte("image-3"), 0755)
	saveContainerNotice(root, State{RolledBack: true, Detail: "previous failure"})
	got, err = PrepareContainerRuntime(root, image, "2.2.5")
	if err != nil || readAll(t, got) != "image-3" {
		t.Fatalf("新版镜像未生效: %v", err)
	}
	if readAll(t, filepath.Join(root, "artex.old")) != "online-2" {
		t.Fatal("镜像升级丢失备份")
	}
	if _, err := os.Stat(filepath.Join(root, "notice.json")); !os.IsNotExist(err) {
		t.Fatal("新镜像继承旧回滚错误")
	}
	os.Remove(filepath.Join(root, "artex.upgrade.json"))
	os.WriteFile(got, []byte("manual-rollback"), 0755)
	os.WriteFile(got+".version", []byte("2.2.4"), 0600)
	_, err = PrepareContainerRuntime(root, image, "2.2.5")
	if err != nil || readAll(t, got) != "manual-rollback" {
		t.Fatal("同一镜像覆盖主动回滚")
	}
}

func TestContainerRuntimeDefersImageWhileUpdatePending(t *testing.T) {
	root := t.TempDir()
	image := filepath.Join(t.TempDir(), "artex")
	os.WriteFile(image, []byte("image-1"), 0755)
	current, err := PrepareContainerRuntime(root, image, "2.2.3")
	if err != nil {
		t.Fatal(err)
	}
	p := pathsForExecutable(current)
	writeMarker(p.Marker, marker{From: "2.2.3", To: "2.2.4"})
	os.WriteFile(p.New, []byte("staged"), 0755)
	os.WriteFile(image, []byte("image-3"), 0755)
	_, err = PrepareContainerRuntime(root, image, "2.2.5")
	if err != nil || readAll(t, current) != "image-1" {
		t.Fatal("镜像覆盖未决更新")
	}
}

func TestContainerRuntimeRejectsRelativeRoot(t *testing.T) {
	if _, err := PrepareContainerRuntime("data/update", "missing", "2.2.3"); err == nil {
		t.Fatal("相对目录必须拒绝")
	}
}

func TestPersistentSwapAndRollbackVersions(t *testing.T) {
	p := testPaths(t)
	os.WriteFile(p.Current, []byte("old"), 0755)
	os.WriteFile(p.Current+".version", []byte("2.2.3"), 0600)
	os.WriteFile(p.New, []byte("new"), 0755)
	writeMarker(p.Marker, marker{From: "2.2.3", To: "2.2.4"})
	if err := swap(p); err != nil {
		t.Fatal(err)
	}
	if readAll(t, p.Current+".version") != "2.2.4" || readAll(t, p.Old+".version") != "2.2.3" {
		t.Fatal("换装版本记录错误")
	}
	if err := rollback(p); err != nil {
		t.Fatal(err)
	}
	if readAll(t, p.Current+".version") != "2.2.3" {
		t.Fatal("回滚版本记录错误")
	}
}

func TestContainerStagedUpdateAndBrokenExecutableRecovery(t *testing.T) {
	requireUnix(t)
	root := t.TempDir()
	image := filepath.Join(t.TempDir(), "artex")
	fakeBin(t, image, "image", 0)
	current, err := PrepareContainerRuntime(root, image, "2.2.3")
	if err != nil {
		t.Fatal(err)
	}
	p := pathsForExecutable(current)
	stage(t, p, "new", 0)
	writeMarker(p.Marker, marker{From: "2.2.3", To: "2.2.4"})
	action, st := applyStaged(p)
	if action != Restart || !st.Pending {
		t.Fatalf("未换装: %v %+v", action, st)
	}
	// 更新后的文件在真正启动前损坏，坏二进制根本无法执行 Bootstrap。
	os.WriteFile(current, []byte("broken executable"), 0755)
	_, err = PrepareContainerRuntime(root, image, "2.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if readAll(t, current) != readAll(t, image) || readVersion(current+".version") != "2.2.3" {
		t.Fatal("未恢复镜像种子的旧版")
	}
	if _, exists := readMarker(p.Marker); exists {
		t.Fatal("失败标记未解除")
	}
	if _, err := os.Stat(filepath.Join(root, "notice.json")); err != nil {
		t.Fatal("重启后丢失回滚结果")
	}
}

func TestContainerNewImageFailureDoesNotRetryForever(t *testing.T) {
	requireUnix(t)
	root := t.TempDir()
	image := filepath.Join(t.TempDir(), "artex")
	fakeBin(t, image, "old", 0)
	current, err := PrepareContainerRuntime(root, image, "2.2.3")
	if err != nil {
		t.Fatal(err)
	}
	original := readAll(t, current)
	fakeBin(t, image, "bad-new-image", 1)
	if _, err = PrepareContainerRuntime(root, image, "2.2.4"); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareContainerRuntime(root, image, "2.2.4"); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareContainerRuntime(root, image, "2.2.4"); err != nil {
		t.Fatal(err)
	}
	if readAll(t, current) != original {
		t.Fatal("反复应用失败的镜像程序")
	}
}

func TestPersistentInterruptedCommitRecovery(t *testing.T) {
	requireUnix(t)
	for _, installed := range []bool{false, true} {
		t.Run(map[bool]string{false: "暂存尚未提交", true: "换装后标记未提交"}[installed], func(t *testing.T) {
			p := testPaths(t)
			fakeBin(t, p.Current, "old", 0)
			stage(t, p, "new", 0)
			writeMarker(p.Marker, marker{From: "2.2.3", To: "2.2.4", Preparing: true})
			if installed {
				os.Rename(p.Current, p.Old)
				os.Rename(p.New, p.Current)
			} else {
				os.Remove(p.New)
			}
			action, st := bootstrap(p)
			if action != Continue {
				t.Fatalf("不应重启: %v", action)
			}
			if installed {
				if !st.Pending || readVersion(p.Current+".version") != "2.2.4" {
					t.Fatalf("未恢复换装状态: %+v", st)
				}
				m, ok := readMarker(p.Marker)
				if !ok || m.Preparing || m.Attempts != 1 {
					t.Fatalf("错误确认: %+v", m)
				}
			} else {
				if !st.FailedStage || st.Pending {
					t.Fatalf("把旧版误当新版: %+v", st)
				}
				if _, ok := readMarker(p.Marker); ok {
					t.Fatal("遗留标记未解除")
				}
			}
		})
	}
}

func TestPersistentManualRollbackKeepsVersionAndCanReverse(t *testing.T) {
	requireUnix(t)
	p := testPaths(t)
	fakeBin(t, p.Current, "new", 0)
	fakeBin(t, p.Old, "old", 0)
	atomicText(p.Current+".version", "2.2.4")
	atomicText(p.Old+".version", "2.2.3")
	newBin, oldBin := readAll(t, p.Current), readAll(t, p.Old)
	if err := manualRollback(p); err != nil {
		t.Fatal(err)
	}
	if readAll(t, p.Current) != oldBin || readVersion(p.Current+".version") != "2.2.3" {
		t.Fatal("程序和版本记录不一致")
	}
	if err := manualRollback(p); err != nil {
		t.Fatal(err)
	}
	if readAll(t, p.Current) != newBin || readVersion(p.Current+".version") != "2.2.4" {
		t.Fatal("反向回滚失败")
	}
}

func TestContainerRestoresCurrentBetweenTwoRenames(t *testing.T) {
	requireUnix(t)
	root := t.TempDir()
	image := filepath.Join(t.TempDir(), "artex")
	fakeBin(t, image, "old", 0)
	current, err := PrepareContainerRuntime(root, image, "2.2.3")
	if err != nil {
		t.Fatal(err)
	}
	p := pathsForExecutable(current)
	stage(t, p, "new", 0)
	writeMarker(p.Marker, marker{From: "2.2.3", To: "2.2.4", Preparing: true})
	os.Rename(p.Current, p.Old)
	if _, err = PrepareContainerRuntime(root, image, "2.2.3"); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(p.Current); err != nil {
		t.Fatal("换装中断后入口程序消失")
	}
	if action, st := bootstrap(p); action != Restart || !st.Pending {
		t.Fatalf("恢复后未完成换装: %v %+v", action, st)
	}
}
