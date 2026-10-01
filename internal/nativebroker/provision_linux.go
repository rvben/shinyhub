//go:build linux

package nativebroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"github.com/rvben/shinyhub/internal/slug"
	"golang.org/x/sys/unix"
)

type ProvisionAction struct {
	Kind string   `json:"kind"`
	Args []string `json:"args,omitempty"`
	Path string   `json:"path,omitempty"`
	UID  int      `json:"uid,omitempty"`
	GID  int      `json:"gid,omitempty"`
	Mode uint32   `json:"mode,omitempty"`
}

func (a ProvisionAction) MarshalJSON() ([]byte, error) {
	type wire struct {
		Kind string   `json:"kind"`
		Args []string `json:"args,omitempty"`
		Path string   `json:"path,omitempty"`
		UID  int      `json:"uid,omitempty"`
		GID  int      `json:"gid,omitempty"`
		Mode string   `json:"mode,omitempty"`
	}
	mode := ""
	if a.Kind == "directory" {
		mode = fmt.Sprintf("%04o", a.Mode)
	}
	return json.Marshal(wire{a.Kind, a.Args, a.Path, a.UID, a.GID, mode})
}

type ProvisionPlan struct {
	Actions []ProvisionAction `json:"actions"`
	Applied bool              `json:"applied"`
	Next    []string          `json:"next"`
}
type provisionAccount struct {
	name     string
	uid, gid int
	shell    string
}
type provisionGroup struct {
	name    string
	gid     int
	members []string
}
type provisionAccounts struct {
	users  map[int]provisionAccount
	groups map[int]provisionGroup
}

func readProvisionAccounts() (provisionAccounts, error) {
	accounts := provisionAccounts{users: map[int]provisionAccount{}, groups: map[int]provisionGroup{}}
	names := map[string]bool{}
	for _, path := range []string{"/etc/passwd", "/etc/group"} {
		if err := secureRegular(path); err != nil {
			return accounts, err
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return accounts, err
		}
		for _, line := range strings.Split(string(raw), "\n") {
			if line == "" {
				continue
			}
			parts := strings.Split(line, ":")
			key := path + ":" + parts[0]
			if names[key] {
				return accounts, errors.New("duplicate local account/group name")
			}
			names[key] = true
			if path == "/etc/passwd" {
				if len(parts) != 7 {
					return accounts, errors.New("unsupported local passwd entry")
				}
				uid, e1 := strconv.Atoi(parts[2])
				gid, e2 := strconv.Atoi(parts[3])
				if e1 != nil || e2 != nil {
					return accounts, errors.New("invalid local account IDs")
				}
				if _, ok := accounts.users[uid]; ok {
					return accounts, errors.New("duplicate local UID")
				}
				accounts.users[uid] = provisionAccount{parts[0], uid, gid, parts[6]}
			} else {
				if len(parts) != 4 {
					return accounts, errors.New("unsupported local group entry")
				}
				gid, err := strconv.Atoi(parts[2])
				if err != nil {
					return accounts, err
				}
				if _, ok := accounts.groups[gid]; ok {
					return accounts, errors.New("duplicate local GID")
				}
				var members []string
				if parts[3] != "" {
					members = strings.Split(parts[3], ",")
				}
				accounts.groups[gid] = provisionGroup{parts[0], gid, members}
			}
		}
	}
	return accounts, nil
}

func provisionIdentityPlan(p Policy, accounts provisionAccounts) ([]ProvisionAction, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	control, ok := accounts.users[p.ControlUID]
	if !ok || control.gid != p.ControlGID {
		return nil, errors.New("controller must already have a local account with its policy primary group")
	}
	if _, ok := accounts.groups[p.ControlGID]; !ok {
		return nil, errors.New("controller group is missing")
	}
	var actions []ProvisionAction
	for _, app := range p.Apps {
		if !slug.Valid(app.Slug) || filepath.Base(app.BundleRoot) != "versions" || filepath.Base(filepath.Dir(app.BundleRoot)) != app.Slug || filepath.Base(app.DataRoot) != app.Slug || filepath.Base(app.CacheRoot) != app.Slug {
			return nil, errors.New("provisioning requires canonical <apps>/<slug>/versions and <data|cache>/<slug> roots")
		}
		name := fmt.Sprintf("shapp-%d-%d", p.ControlUID, app.ID)
		if len(name) > 32 {
			return nil, errors.New("generated app account name exceeds Linux account limit")
		}
		groupName := name
		account, hasAccount := accounts.users[app.UID]
		group, hasGroup := accounts.groups[app.GID]
		// Dangling group references become effective when a missing account or
		// group is created, so inspect them even for a brand-new identity.
		for _, u := range accounts.users {
			if u.gid == app.GID && u.uid != app.UID {
				return nil, errors.New("app primary group is shared with another account")
			}
		}
		for _, g := range accounts.groups {
			for _, member := range g.members {
				if member == name && g.gid != app.GID {
					return nil, errors.New("app account has supplementary groups")
				}
			}
		}
		if hasAccount {
			if account.name != name || account.gid != app.GID || (account.shell != "/usr/sbin/nologin" && account.shell != "/sbin/nologin" && account.shell != "/bin/false") {
				return nil, fmt.Errorf("app %s UID is already used by an incompatible account", app.Slug)
			}
			name = account.name
		} else {
			for _, u := range accounts.users {
				if u.name == name {
					return nil, errors.New("generated app account name already exists")
				}
			}
		}
		if hasGroup {
			if group.name != groupName {
				return nil, errors.New("refuse to reuse an unrelated existing group")
			}
			groupName = group.name
			for _, member := range group.members {
				if member != name && member != control.name {
					return nil, errors.New("app group has unrelated members")
				}
			}

		} else {
			for _, g := range accounts.groups {
				if g.name == groupName {
					return nil, errors.New("generated app group name already exists")
				}
			}
			actions = append(actions, ProvisionAction{Kind: "command", Args: []string{"/usr/sbin/groupadd", "--gid", strconv.Itoa(app.GID), "--", groupName}})
		}
		if !hasAccount {
			actions = append(actions, ProvisionAction{Kind: "command", Args: []string{"/usr/sbin/useradd", "--uid", strconv.Itoa(app.UID), "--gid", groupName, "--no-create-home", "--home-dir", "/nonexistent", "--shell", "/usr/sbin/nologin", "--password", "!", "--", name}})
		}
		member := false
		for _, m := range group.members {
			member = member || m == control.name
		}
		if !member {
			actions = append(actions, ProvisionAction{Kind: "command", Args: []string{"/usr/sbin/usermod", "--append", "--groups", groupName, "--", control.name}})
		}
	}
	return actions, nil
}

func provisionNoACL(path string) error {
	for _, name := range []string{"system.posix_acl_access", "system.posix_acl_default"} {
		n, err := unix.Lgetxattr(path, name, nil)
		if errors.Is(err, unix.ENODATA) || errors.Is(err, unix.EOPNOTSUPP) {
			continue
		}
		if err != nil {
			return err
		}
		if n > 0 {
			return fmt.Errorf("provisioning requires directories without access/default ACLs: %s", path)
		}
	}
	return nil
}

// Missing common directories may be created, but existing ancestors are never
// made more accessible: they may contain controller secrets or unrelated data.
func provisionDirectoryPlan(p Policy) ([]ProvisionAction, error) {
	wanted := map[string]ProvisionAction{}
	boundaries := []string{p.StateDir, p.RuntimeDir}
	for _, a := range p.Apps {
		for _, path := range []string{filepath.Dir(a.BundleRoot), a.DataRoot, a.CacheRoot} {
			for _, other := range boundaries {
				if within(path, other) || within(other, path) {
					return nil, errors.New("provisioning app boundaries must not overlap")
				}
			}
			boundaries = append(boundaries, path)
		}
	}
	for _, a := range p.Apps {
		for _, root := range []string{a.BundleRoot, a.DataRoot, a.CacheRoot} {
			wanted[root] = ProvisionAction{Kind: "directory", Path: root, UID: p.ControlUID, GID: a.GID, Mode: 0770}
		}
		parent := filepath.Dir(a.BundleRoot)
		wanted[parent] = ProvisionAction{Kind: "directory", Path: parent, UID: p.ControlUID, GID: a.GID, Mode: 0711}
	}
	for path := range wanted {
		for ancestor := filepath.Dir(path); ancestor != "/"; ancestor = filepath.Dir(ancestor) {
			if _, ok := wanted[ancestor]; ok {
				continue
			}
			info, err := os.Lstat(ancestor)
			if os.IsNotExist(err) {
				wanted[ancestor] = ProvisionAction{Kind: "directory", Path: ancestor, UID: p.ControlUID, GID: p.ControlGID, Mode: 0711}
				continue
			}
			if err != nil {
				return nil, err
			}
			if err := provisionNoACL(ancestor); err != nil {
				return nil, err
			}
			st := info.Sys().(*syscall.Stat_t)
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || (st.Uid != 0 && int(st.Uid) != p.ControlUID) || info.Mode().Perm()&0022 != 0 || info.Mode().Perm()&0001 == 0 {
				return nil, fmt.Errorf("unsafe or private existing ancestor %s; relocate control files before provisioning", ancestor)
			}
		}
	}
	paths := make([]string, 0, len(wanted))
	for path := range wanted {
		paths = append(paths, path)
	}
	sort.Slice(paths, func(i, j int) bool {
		if len(strings.Split(paths[i], "/")) != len(strings.Split(paths[j], "/")) {
			return len(strings.Split(paths[i], "/")) < len(strings.Split(paths[j], "/"))
		}
		return paths[i] < paths[j]
	})
	var actions []ProvisionAction
	for _, path := range paths {
		action := wanted[path]
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			actions = append(actions, action)
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := provisionNoACL(path); err != nil {
			return nil, err
		}
		st := info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || int(st.Uid) != p.ControlUID || (int(st.Gid) != p.ControlGID && int(st.Gid) != action.GID) {
			return nil, fmt.Errorf("refuse to take over existing directory %s", path)
		}
		if int(st.Gid) != action.GID || uint32(info.Mode().Perm()) != action.Mode || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			actions = append(actions, action)
		}
	}
	return actions, nil
}

func provisionQuiescent(p Policy) error {
	uids := map[int]bool{p.ControlUID: true}
	for _, a := range p.Apps {
		uids[a.UID] = true
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		info, err := os.Stat(filepath.Join("/proc", entry.Name()))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if uids[int(info.Sys().(*syscall.Stat_t).Uid)] {
			return errors.New("stop the controller and every registered app/build/job process before applying provisioning")
		}
	}
	return nil
}

func provisionBrokerAncestors(path string) error {
	for dir := path; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err == nil {
			if !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 {
				return errors.New("broker state/runtime paths require protected root-owned ancestors")
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if dir == "/" {
			return nil
		}
	}
}

// Provision prepares identities/storage only. It does not change controller
// config/secrets, start services, or grant the controller provisioning authority.
func Provision(ctx context.Context, policyPath string, apply bool) (ProvisionPlan, error) {
	plan := ProvisionPlan{Actions: []ProvisionAction{}, Next: []string{"Keep config, database and secrets in controller-private storage; preserve the existing auth key.", "Restart the root broker to load this policy, then restart the controller to load its new group memberships.", "Select runtime.native.broker_socket explicitly; existing contents are normalized at broker/controller startup."}}
	if os.Geteuid() != 0 {
		return plan, errors.New("offline provisioning requires root, including the read-only plan")
	}
	if err := secureRegular(policyPath); err != nil {
		return plan, err
	}
	raw, err := os.ReadFile(policyPath)
	if err != nil {
		return plan, err
	}
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&p); err != nil {
		return plan, err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return plan, errors.New("trailing policy data")
	}
	if err = p.Validate(); err != nil {
		return plan, err
	}
	for _, dir := range []string{p.StateDir, p.RuntimeDir} {
		if err := provisionBrokerAncestors(dir); err != nil {
			return plan, err
		}
	}
	accounts, err := readProvisionAccounts()
	if err != nil {
		return plan, err
	}
	identityActions, err := provisionIdentityPlan(p, accounts)
	if err != nil {
		return plan, err
	}
	for _, a := range p.Apps {
		for _, root := range []string{filepath.Dir(a.BundleRoot), a.DataRoot, a.CacheRoot} {
			if within(policyPath, root) {
				return plan, errors.New("policy must be outside app storage")
			}
		}
	}
	directories, err := provisionDirectoryPlan(p)
	if err != nil {
		return plan, err
	}
	plan.Actions = append(plan.Actions, identityActions...)
	plan.Actions = append(plan.Actions, directories...)
	if !apply {
		return plan, nil
	}
	pid1, e := os.ReadFile("/proc/1/comm")
	if e != nil || strings.TrimSpace(string(pid1)) != "systemd" {
		return plan, errors.New("provisioning apply requires systemd as PID 1")
	}
	if _, e = os.Stat("/sys/fs/cgroup/cgroup.controllers"); e != nil {
		return plan, errors.New("provisioning apply requires cgroup v2")
	}
	if err = provisionQuiescent(p); err != nil {
		return plan, err
	}
	// Refuse an active broker; hold its existing daemon lock through the update.
	// Do not create state directories or lock files during a read-only plan.
	var lock *os.File
	if info, e := os.Lstat(p.StateDir); e == nil {
		if !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return plan, errors.New("unsafe broker state directory")
		}
		lockPath := filepath.Join(p.StateDir, "daemon.lock")
		if err = secureRegular(lockPath); err != nil && !os.IsNotExist(err) {
			return plan, err
		}
		fd, e := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
		if e != nil {
			return plan, e
		}
		lock = os.NewFile(uintptr(fd), lockPath)
		defer lock.Close()
		if err = secureRegular(lockPath); err != nil {
			return plan, err
		}
		if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
			return plan, errors.New("stop the broker before applying provisioning")
		}
	} else if !os.IsNotExist(e) {
		return plan, e
	}
	for _, action := range plan.Actions {
		if err = ctx.Err(); err != nil {
			return plan, err
		}
		if action.Kind == "command" {
			cmd := exec.CommandContext(ctx, action.Args[0], action.Args[1:]...)
			cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
			if out, e := cmd.CombinedOutput(); e != nil {
				return plan, fmt.Errorf("provision %s failed: %w: %s; rerun the plan to inspect partial progress", action.Args[0], e, strings.TrimSpace(string(out)))
			}
		} else {
			// Controller and app accounts are quiescent. Recheck symlinks immediately
			// before each mutation, including directories created earlier in this plan.
			resolved, e := filepath.EvalSymlinks(filepath.Dir(action.Path))
			if e != nil || resolved != filepath.Dir(action.Path) {
				return plan, errors.New("directory ancestor changed during provisioning")
			}
			if e = os.Mkdir(action.Path, 0700); e != nil && !os.IsExist(e) {
				return plan, e
			}
			fd, e := unix.Open(action.Path, unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if e != nil {
				return plan, e
			}
			e = unix.Fchown(fd, action.UID, action.GID)
			if e == nil {
				e = unix.Fchmod(fd, action.Mode)
			}
			unix.Close(fd)
			if e != nil {
				return plan, e
			}
		}
	}
	plan.Applied = true
	return plan, nil
}
