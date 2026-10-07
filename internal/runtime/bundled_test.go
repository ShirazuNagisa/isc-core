package runtime

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// bundleFake 放一棵"包里已经解压好"的运行时树。
//
// 布局与 Scripts/bundle-runtimes.sh 在构建期产出的完全一致：
//
//	<bundleDir>/<kind>/<version>/<Executable>
func bundleFake(t *testing.T, bundleDir string, kind Kind, version string) string {
	t.Helper()
	artifact, ok := ArtifactFor(kind, darwinArm64)
	if !ok {
		t.Fatalf("no artifact for %s", kind)
	}
	root := filepath.Join(bundleDir, string(kind), version)
	exe := filepath.Join(root, artifact.Executable)
	if err := os.MkdirAll(filepath.Dir(exe), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// 包内那份必须能被找到，并且报的是 bundled 而不是 managed。
//
// 这是 App Store 版本唯一能用的运行时来源：沙箱不允许执行数据目录
// （容器）里的任何东西，所以"包里有没有被认出来"直接等于"上架版能不能
// 跑站点"。
func TestResolveFindsAnExtractedRuntimeInTheAppBundle(t *testing.T) {
	m, _ := testManager(t, nil)
	bundleDir := filepath.Join(t.TempDir(), "runtimes")
	root := bundleFake(t, bundleDir, KindNode, "22.14.0")
	m.UseBundle(bundleDir)

	found, ok, err := m.Resolve(context.Background(), KindNode, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("包内有解压好的 node，Resolve 却说没有")
	}
	if found.Source != "bundled" {
		t.Fatalf("Source 应当是 bundled，得到 %q", found.Source)
	}
	if found.Version != "22.14.0" {
		t.Fatalf("版本应当取自目录名，得到 %q", found.Version)
	}
	if found.Root != root {
		t.Fatalf("Root 应当是包内那棵树的根 %q，得到 %q", root, found.Root)
	}
	want := filepath.Join(root, "bin", "node")
	if found.Executable != want {
		t.Fatalf("Executable 应当是 %q，得到 %q", want, found.Executable)
	}
}

// 容器里留着上一份托管副本时，必须仍然选包内的那份。
//
// 这条不是洁癖：直接分发版与上架版共用同一个容器。用户先装了直接分发版
// （运行时落在数据目录里），再换成 App Store 版 —— 那副本还在，可读、
// 可 stat，只有执行会 EPERM。若 Resolve 选中它，用户看到的是"运行时装好了
// 但站点起不来"，而真正该用的是包里那份。
func TestBundledRuntimeWinsOverAManagedCopyInTheDataDirectory(t *testing.T) {
	m, _ := testManager(t, nil)
	installFake(t, m, KindNode, "22.14.0", "bin/node")

	bundleDir := filepath.Join(t.TempDir(), "runtimes")
	bundleFake(t, bundleDir, KindNode, "22.14.0")
	m.UseBundle(bundleDir)

	found, ok, err := m.Resolve(context.Background(), KindNode, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("两份都在，Resolve 却说没有")
	}
	if found.Source != "bundled" {
		t.Fatalf("包内优先，Source 应当是 bundled，得到 %q（Root=%s）", found.Source, found.Root)
	}
}

// 没有内置运行时的构建不受影响：照旧用数据目录里那份。
func TestManagedRuntimeStillResolvesWhenNothingIsBundled(t *testing.T) {
	m, _ := testManager(t, nil)
	dir := installFake(t, m, KindNode, "22.14.0", "bin/node")

	found, ok, err := m.Resolve(context.Background(), KindNode, "")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || found.Source != "managed" || found.Root != dir {
		t.Fatalf("应当回落到 managed，得到 ok=%v %+v", ok, found)
	}
}

// 目录在、可执行文件不在 —— 不算数。
//
// 半解压的树是真实会发生的事（构建中断、杀进程），而认下它的后果是
// 站点启动时报一个与"包没打完"毫不相干的错。
func TestBundledTreeWithoutItsExecutableIsIgnored(t *testing.T) {
	m, _ := testManager(t, nil)
	bundleDir := filepath.Join(t.TempDir(), "runtimes")
	if err := os.MkdirAll(filepath.Join(bundleDir, "node", "22.14.0", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	m.UseBundle(bundleDir)

	if _, ok, _ := m.Resolve(context.Background(), KindNode, ""); ok {
		t.Fatal("缺可执行文件的树不该被认下")
	}
}

// 版本不足时不能被选中，和托管那条路一致。
func TestBundledRuntimeRespectsMinVersion(t *testing.T) {
	m, _ := testManager(t, nil)
	bundleDir := filepath.Join(t.TempDir(), "runtimes")
	bundleFake(t, bundleDir, KindNode, "20.11.0")
	m.UseBundle(bundleDir)

	if _, ok, _ := m.Resolve(context.Background(), KindNode, "22.0.0"); ok {
		t.Fatal("20.11.0 不满足 >= 22.0.0，不该被选中")
	}
	if _, ok, _ := m.Resolve(context.Background(), KindNode, "18.0.0"); !ok {
		t.Fatal("20.11.0 满足 >= 18.0.0，应当被选中")
	}
}

// 同一类型下取版本最高的那个目录。
func TestBundledRuntimePicksTheHighestVersion(t *testing.T) {
	m, _ := testManager(t, nil)
	bundleDir := filepath.Join(t.TempDir(), "runtimes")
	bundleFake(t, bundleDir, KindNode, "20.11.0")
	newest := bundleFake(t, bundleDir, KindNode, "22.14.0")
	m.UseBundle(bundleDir)

	found, ok, _ := m.Resolve(context.Background(), KindNode, "")
	if !ok || found.Root != newest {
		t.Fatalf("应当取 22.14.0 那棵，得到 ok=%v %+v", ok, found)
	}
}

// Provision 看到包内那份就该直接返回 —— 一个字节都不下载、不写容器。
//
// "不写容器"本身是这条断言的一半：App Store 版写进容器的东西执行不了，
// 所以供给路径必须在这里就结束。
func TestProvisionReturnsTheBundledRuntimeWithoutTouchingDisk(t *testing.T) {
	m, dataRoot := testManager(t, nil)
	bundleDir := filepath.Join(t.TempDir(), "runtimes")
	root := bundleFake(t, bundleDir, KindNode, "22.14.0")
	m.UseBundle(bundleDir)

	found, err := m.Provision(context.Background(), KindNode, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if found.Source != "bundled" || found.Root != root {
		t.Fatalf("应当直接用包内那份，得到 %+v", found)
	}
	if entries, err := os.ReadDir(filepath.Join(dataRoot, "runtimes")); err == nil && len(entries) > 0 {
		t.Fatalf("供给内置运行时不该往数据目录写东西，却出现了 %d 项", len(entries))
	}
}

// /v1/runtimes 上要能看到内置的那份（界面据此显示"已就绪"）。
func TestInventoryListsTheBundledRuntime(t *testing.T) {
	m, _ := testManager(t, nil)
	bundleDir := filepath.Join(t.TempDir(), "runtimes")
	bundleFake(t, bundleDir, KindNode, "22.14.0")
	m.UseBundle(bundleDir)

	items, err := m.Inventory(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.Kind == KindNode {
			if item.Source != "bundled" {
				t.Fatalf("node 的来源应当是 bundled，得到 %q", item.Source)
			}
			return
		}
	}
	t.Fatal("清单里没有 node")
}

// Availability 要跟着 Resolve 一起认内置的那份，否则界面会把跑得了的预设
// 标成"跑不了"。
func TestAvailabilityReportsBundledRuntimeAsAvailable(t *testing.T) {
	m, _ := testManager(t, nil)
	bundleDir := filepath.Join(t.TempDir(), "runtimes")
	bundleFake(t, bundleDir, KindNode, "22.14.0")
	m.UseBundle(bundleDir)

	got := m.Availability(context.Background(), KindNode, "")
	if !got.Available || got.Source != "bundled" {
		t.Fatalf("内置的 node 应当报可用（source=bundled），得到 %+v", got)
	}
}
