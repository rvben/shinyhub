//go:build linux

package nativebroker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"testing"
)

// Disposable Linux VM only; accounts are retained so retired UIDs are not reused.
func TestProvisionLive(t *testing.T) {
	if os.Getenv("NATIVE_BROKER_PROVISION_TEST") != "1" {
		t.Skip("disposable provisioning fixture not enabled")
	}
	if os.Getuid() != 0 {
		t.Fatal("fixture requires root")
	}
	const base = "/var/lib/shinyhub-provision-test"
	if _, err := os.Stat(base); !os.IsNotExist(err) {
		t.Fatal("refuse existing provisioning fixture")
	}
	if out, err := exec.Command("/usr/sbin/useradd", "--uid", "22200", "--user-group", "--no-create-home", "--shell", "/usr/sbin/nologin", "shprovision-control").CombinedOutput(); err != nil {
		t.Fatalf("controller: %s %v", out, err)
	}
	if err := os.Mkdir(base, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	p := Policy{ControlUID: 22200, ControlGID: 22200, RuntimeDir: "/run/shinyhub-provision-test", Socket: "/run/shinyhub-provision-test/control.sock", StateDir: "/var/lib/shinyhub-provision-test-broker"}
	for i, name := range []string{"alpha", "beta"} {
		uid := 22201 + i
		p.Apps = append(p.Apps, App{ID: int64(i + 1), Slug: name, UID: uid, GID: uid, BundleRoot: base + "/apps/" + name + "/versions", DataRoot: base + "/data/" + name, CacheRoot: base + "/cache/" + name})
	}
	raw, _ := json.Marshal(p)
	path := base + "/policy.json"
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	plan, err := Provision(ctx, path, false)
	if err != nil || len(plan.Actions) == 0 {
		t.Fatalf("plan: %v %+v", err, plan)
	}
	if _, err := os.Stat(base + "/apps"); !os.IsNotExist(err) {
		t.Fatal("dry run created storage")
	}
	blocker := exec.Command("/usr/bin/setpriv", "--reuid=22200", "--regid=22200", "--clear-groups", "/bin/sleep", "60")
	if err := blocker.Start(); err != nil {
		t.Fatal(err)
	}
	if _, err = Provision(ctx, path, true); err == nil {
		t.Fatal("running controller accepted")
	}
	_ = blocker.Process.Kill()
	_ = blocker.Wait()
	plan, err = Provision(ctx, path, true)
	if err != nil || !plan.Applied {
		t.Fatalf("apply: %v %+v", err, plan)
	}
	repeat, err := Provision(ctx, path, false)
	if err != nil || len(repeat.Actions) != 0 {
		t.Fatalf("not idempotent: %v %+v", err, repeat)
	}
	for _, a := range p.Apps {
		info, err := os.Stat(a.BundleRoot)
		if err != nil {
			t.Fatal(err)
		}
		st := info.Sys().(*syscall.Stat_t)
		if st.Uid != 22200 || int(st.Gid) != a.GID || info.Mode().Perm() != 0770 {
			t.Fatalf("incorrect private ownership: %v", info)
		}
		probe := exec.Command("/usr/bin/setpriv", "--reuid="+strconv.Itoa(a.UID), "--regid="+strconv.Itoa(a.GID), "--clear-groups", "/usr/bin/python3", "-c", "import os; open('"+a.DataRoot+"/probe','w').write(str(os.getuid()))")
		if out, err := probe.CombinedOutput(); err != nil {
			t.Fatalf("own data: %s %v", out, err)
		}
	}
	t.Run("private ancestor is not widened", func(t *testing.T) {
		apps := base + "/apps"
		if err := os.Chmod(apps, 0700); err != nil {
			t.Fatal(err)
		}
		defer os.Chmod(apps, 0711)
		if _, err := Provision(ctx, path, false); err == nil {
			t.Fatal("private common ancestor accepted")
		}
		info, _ := os.Stat(apps)
		if info.Mode().Perm() != 0700 {
			t.Fatal("private directory was made public")
		}
	})
	t.Run("symlink cannot be taken over", func(t *testing.T) {
		root := p.Apps[0].CacheRoot
		old := root + "-old"
		if err := os.Rename(root, old); err != nil {
			t.Fatal(err)
		}
		defer os.Rename(old, root)
		if err := os.Symlink("/etc", root); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(root)
		if _, err := Provision(ctx, path, true); err == nil {
			t.Fatal("symlink app root accepted")
		}
	})
	// The created account names bind the controller UID and database app ID;
	// pointing a different app at the old UID must fail rather than reuse it.
	p.Apps[0].ID = 3
	raw, _ = json.Marshal(p)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Provision(ctx, path, false); err == nil {
		t.Fatal("retired app UID reassigned")
	}
	t.Log("plan, apply, repeat, write access, running-process refusal, symlink/private-ancestor refusal and UID-reuse refusal passed")
}

func TestProvisionDefaultACLRefused(t *testing.T) {
	if os.Getenv("NATIVE_BROKER_PROVISION_TEST") != "1" {
		t.Skip("disposable provisioning fixture not enabled")
	}
	base, err := os.MkdirTemp("/var/lib", "shinyhub-provision-acl-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(base)
	if err = os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	// Linux POSIX ACL xattr: owner, named user, owning group, mask, other.
	raw := []byte{2, 0, 0, 0}
	for _, entry := range []struct {
		tag, perm uint16
		id        uint32
	}{{1, 7, 0xffffffff}, {2, 4, 22202}, {4, 0, 0xffffffff}, {16, 4, 0xffffffff}, {32, 0, 0xffffffff}} {
		raw = binary.LittleEndian.AppendUint16(raw, entry.tag)
		raw = binary.LittleEndian.AppendUint16(raw, entry.perm)
		raw = binary.LittleEndian.AppendUint32(raw, entry.id)
	}
	if err = unix.Setxattr(base, "system.posix_acl_default", raw, 0); err != nil {
		t.Fatal(err)
	}
	p, _ := provisionFixture()
	p.Apps[0].BundleRoot = base + "/apps/alpha/versions"
	p.Apps[0].DataRoot = base + "/data/alpha"
	p.Apps[0].CacheRoot = base + "/cache/alpha"
	if _, err = provisionDirectoryPlan(p); err == nil {
		t.Fatal("inherited ACL could expose private app storage")
	}
	if _, err = os.Stat(base + "/apps"); !os.IsNotExist(err) {
		t.Fatal("ACL refusal modified storage")
	}
}

func TestProvisionBrokerLock(t *testing.T) {
	if os.Getenv("NATIVE_BROKER_PROVISION_TEST") != "1" {
		t.Skip("disposable provisioning fixture not enabled")
	}
	const path = "/var/lib/shinyhub-provision-test/policy.json"
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var p Policy
	if err = json.Unmarshal(raw, &p); err != nil {
		t.Fatal(err)
	}
	p.Apps[0].ID = 1
	raw, _ = json.Marshal(p)
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(p.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	plan, err := Provision(context.Background(), path, false)
	if err != nil || len(plan.Actions) != 0 {
		t.Fatalf("repeat plan: %+v %v", plan, err)
	}
	if _, err = Provision(context.Background(), path, true); err != nil {
		t.Fatalf("empty precreated state: %v", err)
	}
	lock, err := os.OpenFile(p.StateDir+"/daemon.lock", os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if _, err = Provision(context.Background(), path, true); err == nil {
		t.Fatal("active broker lock bypassed")
	}
}
