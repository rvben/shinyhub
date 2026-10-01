//go:build linux

package nativebroker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

var unitPattern = regexp.MustCompile(`^shinyhub-isolated-[0-9]+-[0-9a-f]{32}\.service$`)

type record struct {
	Helper string            `json:"helper,omitempty"`
	Unit   string            `json:"unit"`
	AppID  int64             `json:"app_id"`
	PID    int               `json:"pid"`
	Birth  string            `json:"birth"`
	Boot   string            `json:"boot"`
	Labels map[string]string `json:"labels"`
	Final  *State            `json:"final,omitempty"`
}
type Server struct {
	policy Policy
	binary string
	boot   string
	mu     sync.Mutex
}

func secureRegular(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("broker privileged paths must be absolute and canonical")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || st.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 {
		return errors.New("broker binary and policy must be root-owned regular files, not writable by others")
	}
	// Also protect every ancestor against replacing an otherwise trusted file.
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err = os.Lstat(dir)
		if err != nil {
			return err
		}
		st = info.Sys().(*syscall.Stat_t)
		if !info.IsDir() || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return errors.New("broker binary and policy require root-owned, non-writable ancestors")
		}
		if dir == "/" {
			break
		}
	}
	return nil
}
func NewServer(policyPath string) (*Server, error) {
	if os.Geteuid() != 0 {
		return nil, errors.New("native broker requires root; no fallback")
	}
	if err := secureRegular(policyPath); err != nil {
		return nil, err
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return nil, err
	}
	if err = secureRegular(binary); err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(policyPath)
	if err != nil {
		return nil, err
	}
	var p Policy
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err = dec.Decode(&p); err != nil {
		return nil, errors.New("invalid broker policy")
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, errors.New("trailing broker policy data")
	}
	if err = p.Validate(); err != nil {
		return nil, err
	}
	pid1, err := os.ReadFile("/proc/1/comm")
	if err != nil || strings.TrimSpace(string(pid1)) != "systemd" {
		return nil, errors.New("systemd must be PID 1; no fallback")
	}
	if _, err = os.Stat("/sys/fs/cgroup/cgroup.controllers"); err != nil {
		return nil, errors.New("cgroup v2 is required; no fallback")
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{p.RuntimeDir, p.StateDir} {
		if err = os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(dir)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("broker state/runtime directories must be root-owned and protected")
		}

		for ancestor := filepath.Dir(dir); ; ancestor = filepath.Dir(ancestor) {
			info, err := os.Lstat(ancestor)
			if err != nil {
				return nil, err
			}
			if !info.IsDir() || info.Sys().(*syscall.Stat_t).Uid != 0 || info.Mode().Perm()&0022 != 0 {
				return nil, errors.New("broker state/runtime ancestors must be root-owned and protected")
			}
			if ancestor == "/" {
				break
			}
		}
	}
	// Workers can traverse to their PID-authenticated handoff sockets. The
	// control socket itself remains 0600 and authenticates peer credentials.
	if err = os.Chmod(p.RuntimeDir, 0755); err != nil {
		return nil, err
	}
	if err = os.Chmod(p.StateDir, 0700); err != nil {
		return nil, err
	}
	for _, a := range p.Apps {
		account, err := user.LookupId(strconv.Itoa(a.UID))
		if err != nil {
			return nil, errors.New("registered app UID must have a local account")
		}
		if account.Gid != strconv.Itoa(a.GID) {
			return nil, errors.New("app primary group does not match policy")
		}
		groups, err := account.GroupIds()
		if err != nil {
			return nil, err
		}
		for _, g := range groups {
			if g != strconv.Itoa(a.GID) {
				return nil, errors.New("app accounts must not have supplementary groups")
			}
		}
		for _, root := range []string{a.BundleRoot, a.DataRoot, a.CacheRoot} {
			if err = validateRoot(root, p.ControlUID, a.UID); err != nil {
				return nil, err
			}
			if within(binary, root) || within(policyPath, root) {
				return nil, errors.New("privileged broker files must be outside app storage")
			}
		}
	}
	return &Server{policy: p, binary: binary, boot: strings.TrimSpace(string(boot))}, nil
}
func validateRoot(path string, controlUID, appUID int) error {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return err
	}
	if resolved != path {
		return errors.New("app roots must not contain symbolic links")
	}
	for dir := path; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err != nil {
			return err
		}
		st := info.Sys().(*syscall.Stat_t)
		allowed := st.Uid == 0 || int(st.Uid) == controlUID
		if dir == path {
			allowed = allowed || int(st.Uid) == appUID
		}
		if !info.IsDir() || !allowed || info.Mode().Perm()&0002 != 0 {
			return errors.New("app root ancestors must be controlled by root or the control plane")
		}
		if dir != "/" && dir != path && info.Mode().Perm()&0020 != 0 && int(st.Gid) != 0 {
			return errors.New("app root ancestors must not be group-writable")
		}
		if dir == "/" {
			break
		}
	}
	return nil
}
func system(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("system service operation failed: %w", err)
	}
	return string(out), nil
}
func properties(ctx context.Context, unit string) (map[string]string, error) {
	out, err := system(ctx, "/usr/bin/systemctl", "show", unit, "--property=MainPID,ExecMainPID,ExecMainCode,ExecMainStatus,ActiveState,SubState,LoadState,ControlGroup,FreezerState,User,Group,NoNewPrivileges,PrivateTmp,PrivateDevices,ProtectSystem,ProtectHome,ProtectControlGroups,ProtectProc,MemoryMax,TasksMax,CPUQuotaPerSecUSec")
	if err != nil {
		return nil, err
	}
	result := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			result[k] = v
		}
	}
	return result, nil
}
func birth(pid int) (string, error) {
	raw, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return "", err
	}
	i := bytes.LastIndexByte(raw, ')')
	if i < 0 {
		return "", errors.New("invalid process stat")
	}
	fields := strings.Fields(string(raw[i+1:]))
	if len(fields) < 20 {
		return "", errors.New("invalid process stat")
	}
	return fields[19], nil // field 22, counting from field 3 after comm
}
func (s *Server) save(r record) error {
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(s.policy.StateDir, ".record-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(name, filepath.Join(s.policy.StateDir, r.Unit+".json")); err != nil {
		return err
	}
	d, err := os.Open(s.policy.StateDir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func (s *Server) load(unit string) (record, error) {
	if !unitPattern.MatchString(unit) {
		return record{}, errors.New("invalid isolated unit identity")
	}
	raw, err := os.ReadFile(filepath.Join(s.policy.StateDir, unit+".json"))
	if err != nil {
		return record{}, errors.New("unknown isolated unit")
	}
	var r record
	if json.Unmarshal(raw, &r) != nil || r.Unit != unit {
		return record{}, errors.New("invalid isolated unit record")
	}
	return r, nil
}
func (s *Server) status(ctx context.Context, r record) (State, error) {
	if r.Final != nil {
		return *r.Final, nil
	}
	if r.Boot != s.boot {
		return State{Unit: r.Unit, Labels: r.Labels, Code: -1}, nil
	}
	p, err := properties(ctx, r.Unit)
	if err != nil {
		return State{}, err
	}
	result := State{Unit: r.Unit, Labels: r.Labels}
	result.PID, _ = strconv.Atoi(p["MainPID"])
	result.Frozen = p["FreezerState"] == "frozen"
	result.Active = result.PID > 0
	if result.PID > 0 {
		b, err := birth(result.PID)
		if errors.Is(err, os.ErrNotExist) {
			result.PID = 0
			result.Active = false
		} else if err != nil || result.PID != r.PID || b != r.Birth {
			return State{}, errors.New("worker identity changed; refusing operation")
		}
		if result.Active {
			exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", result.PID))
			if errors.Is(err, os.ErrNotExist) {
				result.PID = 0
				result.Active = false
			} else if err != nil {
				return State{}, errors.New("cannot verify worker executable")
			}
			helper := r.Helper
			if helper == "" {
				helper = s.binary
			}
			result.Ready = result.Active && strings.TrimSuffix(exe, " (deleted)") != helper
		}
	}
	if cg := p["ControlGroup"]; cg != "" {
		if !within(filepath.Clean("/sys/fs/cgroup"+cg), "/sys/fs/cgroup/system.slice") {
			return State{}, errors.New("unexpected worker cgroup")
		}
		events, err := os.ReadFile(filepath.Clean("/sys/fs/cgroup"+cg) + "/cgroup.events")
		if err == nil {
			result.Populated = strings.Contains(string(events), "populated 1")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return State{}, err
		}
	}
	result.Code, _ = strconv.Atoi(p["ExecMainStatus"])
	code, _ := strconv.Atoi(p["ExecMainCode"])
	result.Signaled = code == 2 || code == 3
	return result, nil
}

// normalize opens every inode without following links and changes only files
// in the registered app tree. Private app groups let the control plane retain normal upload/backup access without giving apps its
// group. App-created files remain accessible through the controller's explicit
// membership of each registered app group.
func normalize(root string, controlUID, uid, gid int) error {
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	count := 0
	var walk func(int) error
	walk = func(fd int) error {
		f := os.NewFile(uintptr(fd), "app-directory")
		defer f.Close()
		var st unix.Stat_t
		if err := unix.Fstat(fd, &st); err != nil {
			return err
		}
		if st.Uid != 0 && int(st.Uid) != controlUID && int(st.Uid) != uid {
			return errors.New("app tree contains an inode owned by another identity")
		}
		if err := unix.Fchown(fd, controlUID, gid); err != nil {
			return err
		}
		if err := unix.Fchmod(fd, 0770); err != nil {
			return err
		}
		entries, err := f.ReadDir(-1)
		if err != nil {
			return err
		}
		for _, entry := range entries {
			count++
			if count > 1000000 {
				return errors.New("app tree exceeds preparation limit")
			}
			if entry.Type()&os.ModeSymlink != 0 {
				continue
			}
			child, err := unix.Openat(fd, entry.Name(), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
			if err != nil {
				return err
			}
			if err = unix.Fstat(child, &st); err != nil {
				unix.Close(child)
				return err
			}
			if st.Mode&unix.S_IFMT == unix.S_IFDIR {
				if err = walk(child); err != nil {
					return err
				}
				continue
			}
			if st.Mode&unix.S_IFMT != unix.S_IFREG || (st.Uid != 0 && int(st.Uid) != controlUID && int(st.Uid) != uid) {
				unix.Close(child)
				return errors.New("app tree contains unsupported files")
			}
			// An app can create links to its own inodes. A pre-existing hardlink
			// owned by root/the controller is only safe when it is already in
			// this app's private group; never grant an app access to another group.
			if int(st.Uid) != uid && st.Nlink > 1 && int(st.Gid) != gid {
				unix.Close(child)
				return errors.New("controller-owned hardlinks are not supported in isolated app storage")
			}
			mode := uint32(0660)
			if st.Mode&0111 != 0 {
				mode = 0770
			}
			err = unix.Fchown(child, controlUID, gid)
			if err == nil {
				err = unix.Fchmod(child, mode)
			}
			unix.Close(child)
			if err != nil {
				return err
			}
		}
		return nil
	}
	return walk(fd)
}

func (s *Server) launch(ctx context.Context, l *Launch, files []*os.File) (State, error) {
	a, err := s.policy.validateLaunch(l, len(files))
	if err != nil {
		return State{}, err
	}
	for _, root := range []string{a.BundleRoot, a.DataRoot, a.CacheRoot} {
		if err = validateRoot(root, s.policy.ControlUID, a.UID); err != nil {
			return State{}, err
		}
		if err = normalize(root, s.policy.ControlUID, a.UID, a.GID); err != nil {
			return State{}, err
		}
	}
	// A working directory must be a real directory beneath the pinned app root.
	resolved, err := filepath.EvalSymlinks(l.Dir)
	if err != nil || resolved != l.Dir {
		return State{}, errors.New("working directory must not contain symlinks")
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return State{}, err
	}
	unit := fmt.Sprintf("shinyhub-isolated-%d-%s.service", a.ID, hex.EncodeToString(random[:]))
	gatePath := filepath.Join(s.policy.RuntimeDir, hex.EncodeToString(random[:])+".sock")
	gate, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: gatePath, Net: "unixpacket"})
	if err != nil {
		return State{}, err
	}
	defer gate.Close()
	defer os.Remove(gatePath)
	if err = os.Chown(gatePath, a.UID, a.GID); err != nil {
		return State{}, err
	}
	if err = os.Chmod(gatePath, 0600); err != nil {
		return State{}, err
	}
	// Actual argv and secret env never enter systemd's public unit properties.
	l.Env = append(l.Env, "HOME="+a.CacheRoot, "XDG_CACHE_HOME="+a.CacheRoot, "UV_CACHE_DIR="+filepath.Join(a.CacheRoot, "uv"), "UV_PYTHON_INSTALL_DIR="+filepath.Join(a.CacheRoot, "python"), "RENV_PATHS_ROOT="+filepath.Join(a.CacheRoot, "renv"))
	cred, err := os.CreateTemp(s.policy.StateDir, ".launch-")
	if err != nil {
		return State{}, err
	}
	credPath := cred.Name()
	defer os.Remove(credPath)
	if err = json.NewEncoder(cred).Encode(l); err != nil {
		cred.Close()
		return State{}, err
	}
	if err = cred.Close(); err != nil {
		return State{}, err
	}
	props := map[string]string{
		"User": strconv.Itoa(a.UID), "Group": strconv.Itoa(a.GID), "Type": "exec", "RemainAfterExit": "yes",
		"WorkingDirectory": l.Dir, "UMask": "0007", "NoNewPrivileges": "yes", "PrivateTmp": "yes", "PrivateDevices": "yes", "PrivateMounts": "yes",
		"ProtectSystem": "strict", "ProtectHome": "yes", "ProtectControlGroups": "yes", "ProtectKernelTunables": "yes", "ProtectKernelModules": "yes", "ProtectProc": "invisible",
		"CapabilityBoundingSet": "", "AmbientCapabilities": "", "RestrictNamespaces": "yes", "RestrictAddressFamilies": "AF_UNIX AF_INET AF_INET6",
		"KillMode": "control-group", "TimeoutStopSec": "10s", "TasksMax": "512", "MemoryAccounting": "yes", "CPUAccounting": "yes",
		"ReadWritePaths": strings.Join([]string{a.BundleRoot, a.DataRoot, a.CacheRoot}, " "),
		"LoadCredential": "launch.json:" + credPath,
	}
	if l.MemoryMB > 0 {
		props["MemoryMax"] = strconv.Itoa(l.MemoryMB) + "M"
	}
	if l.CPUPercent > 0 {
		props["CPUQuota"] = strconv.Itoa(l.CPUPercent) + "%"
	}
	argv := []string{"/usr/bin/systemd-run", "--quiet", "--unit=" + unit}
	for key, value := range props {
		argv = append(argv, "--property="+key+"="+value)
	}
	argv = append(argv, "--", s.binary, "bootstrap", gatePath)
	success := false
	defer func() {
		if !success {
			stopCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, _ = system(stopCtx, "/usr/bin/systemctl", "stop", unit)
		}
	}()
	if _, err = system(ctx, argv...); err != nil {
		return State{}, err
	}
	p, err := properties(ctx, unit)
	if err != nil {
		return State{}, err
	}
	for _, key := range []string{"User", "Group", "NoNewPrivileges", "PrivateTmp", "PrivateDevices", "ProtectSystem", "ProtectHome", "ProtectControlGroups", "ProtectProc"} {
		if p[key] != props[key] {
			return State{}, errors.New("required worker protection was not applied")
		}
	}
	pid, err := strconv.Atoi(p["MainPID"])
	if err != nil || pid <= 0 {
		return State{}, errors.New("worker bootstrap did not start")
	}
	b, err := birth(pid)
	if err != nil {
		return State{}, err
	}
	r := record{Unit: unit, AppID: a.ID, PID: pid, Birth: b, Boot: s.boot, Helper: s.binary, Labels: l.Labels}
	if err = s.save(r); err != nil {
		return State{}, err
	}
	_ = gate.SetDeadline(time.Now().Add(10 * time.Second))
	for {
		c, err := gate.AcceptUnix()
		if err != nil {
			return State{}, errors.New("worker descriptor handoff failed")
		}
		uid, peerPID, err := peerUID(c)
		if err != nil || uid != a.UID || peerPID != pid {
			c.Close()
			continue
		}
		err = sendPacket(c, []byte("ready"), files)
		c.Close()
		if err != nil {
			return State{}, err
		}
		break
	}
	success = true
	return State{Unit: unit, PID: pid, Active: true, Populated: true, Labels: l.Labels}, nil
}
func (s *Server) dispatch(ctx context.Context, r Request, files []*os.File) (Response, error) {
	if r.Op != "suspend" && r.ReclaimFraction != 0 {
		return Response{}, errors.New("unexpected reclaim settings")
	}
	if r.Op != "limits" && r.Limits != nil {
		return Response{}, errors.New("unexpected resource settings")
	}
	if r.Op != "list" && r.Cursor != "" {
		return Response{}, errors.New("unexpected inventory cursor")
	}
	if r.Op == "start" {
		if r.Unit != "" || r.Signal != 0 {
			return Response{}, errors.New("unexpected launch fields")
		}
		state, err := s.launch(ctx, r.Launch, files)
		return Response{State: &state}, err
	}
	if len(files) > 0 || r.Launch != nil {
		return Response{}, errors.New("unexpected descriptors or launch fields")
	}
	if r.Op == "prepare" {
		if r.Unit != "" || r.Signal != 0 {
			return Response{}, errors.New("unexpected preparation fields")
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		for _, a := range s.policy.Apps {
			for _, root := range []string{a.BundleRoot, a.DataRoot, a.CacheRoot} {
				if err := validateRoot(root, s.policy.ControlUID, a.UID); err != nil {
					return Response{}, err
				}
				if err := normalize(root, s.policy.ControlUID, a.UID, a.GID); err != nil {
					return Response{}, err
				}
			}
		}
		return Response{}, nil
	}
	if r.Op == "hello" {
		if r.Unit != "" || r.Signal != 0 {
			return Response{}, errors.New("unexpected hello fields")
		}
		return Response{Policy: &s.policy}, nil
	}
	if r.Op == "list" {
		if r.Cursor != "" && !unitPattern.MatchString(r.Cursor) {
			return Response{}, errors.New("invalid inventory cursor")
		}
		if r.Unit != "" || r.Signal != 0 {
			return Response{}, errors.New("unexpected inventory fields")
		}
		entries, err := os.ReadDir(s.policy.StateDir)
		if err != nil {
			return Response{}, err
		}
		out := Response{}
		bytesUsed := 1024
		for _, e := range entries {
			if !strings.HasSuffix(e.Name(), ".service.json") {
				continue
			}
			if r.Cursor != "" && e.Name() <= r.Cursor+".json" {
				continue
			}
			rec, err := s.load(strings.TrimSuffix(e.Name(), ".json"))
			if err != nil {
				return out, err
			}
			if rec.Final != nil {
				continue
			}
			state, err := s.status(ctx, rec)
			if err != nil {
				return out, err
			}
			raw, err := json.Marshal(state)
			if err != nil {
				return out, err
			}
			if bytesUsed+len(raw) > MaxMessage {
				if len(out.States) == 0 {
					return out, errors.New("worker inventory record is too large")
				}
				out.NextCursor = out.States[len(out.States)-1].Unit
				break
			}
			bytesUsed += len(raw) + 1
			out.States = append(out.States, state)
		}
		return out, nil
	}
	if r.Op != "status" && r.Op != "stop" && r.Op != "signal" && r.Op != "freeze" && r.Op != "thaw" && r.Op != "suspend" && r.Op != "limits" {
		return Response{}, errors.New("unknown broker operation")
	}
	if r.Op != "signal" && r.Signal != 0 {
		return Response{}, errors.New("unexpected signal field")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, err := s.load(r.Unit)
	if err != nil {
		return Response{}, err
	}
	state, err := s.status(ctx, rec)
	if err != nil {
		return Response{}, err
	}
	if r.Op == "status" {
		return Response{State: &state}, nil
	}
	if rec.Final != nil {
		return Response{State: &state}, nil
	}
	if r.Op == "suspend" {
		if !state.Active || !state.Ready {
			return Response{}, errors.New("worker is not ready")
		}
		freed, err := s.suspend(ctx, rec, r.ReclaimFraction)
		return Response{Freed: freed}, err
	}
	if r.Op == "limits" {
		if !state.Active || r.Limits == nil {
			return Response{}, errors.New("active worker and resource settings are required")
		}
		args := []string{"/usr/bin/systemctl", "set-property", "--runtime", r.Unit}
		if v := r.Limits.MemoryMB; v != nil {
			if *v < 0 || *v > 1048576 {
				return Response{}, errors.New("invalid memory limit")
			}
			value := "infinity"
			if *v > 0 {
				value = strconv.Itoa(*v) + "M"
			}
			args = append(args, "MemoryMax="+value)
		}
		if v := r.Limits.CPUPercent; v != nil {
			if *v < 0 || *v > 100000 {
				return Response{}, errors.New("invalid CPU limit")
			}
			value := "" // An empty assignment resets CPUQuota to unlimited.
			if *v > 0 {
				value = strconv.Itoa(*v) + "%"
			}
			args = append(args, "CPUQuota="+value)
		}
		if len(args) > 4 {
			_, err = system(ctx, args...)
		}
		return Response{}, err
	}
	if r.Op == "signal" {
		if r.Signal != int(unix.SIGTERM) && r.Signal != int(unix.SIGKILL) && r.Signal != int(unix.SIGCONT) {
			return Response{}, errors.New("signal is not allowlisted")
		}
		// TERM/KILL stop the whole unit, including detached descendants.
		if r.Signal == int(unix.SIGCONT) {
			r.Op = "thaw"
		} else {
			r.Op = "stop"
		}
	}
	if r.Op == "freeze" || r.Op == "thaw" {
		if !state.Active {
			return Response{}, errors.New("worker is not active")
		}
		if _, err = system(ctx, "/usr/bin/systemctl", r.Op, r.Unit); err != nil {
			return Response{}, err
		}
		state, err = s.status(ctx, rec)
		return Response{State: &state}, err
	}
	// Freeze must never make a unit impossible to stop.
	if state.Frozen {
		if _, err = system(ctx, "/usr/bin/systemctl", "thaw", r.Unit); err != nil {
			return Response{}, err
		}
	}
	if state.Active || state.Populated {
		if r.Signal == int(unix.SIGKILL) {
			if _, err = system(ctx, "/usr/bin/systemctl", "kill", "--kill-whom=all", "--signal=KILL", r.Unit); err != nil {
				return Response{}, err
			}
		}
		if _, err = system(ctx, "/usr/bin/systemctl", "stop", r.Unit); err != nil {
			return Response{}, err
		}
	}
	final, err := s.status(ctx, rec)
	if err != nil {
		return Response{}, err
	}
	if final.Active || final.Populated {
		return Response{}, errors.New("worker stop could not be confirmed")
	}
	// Capture the exit status before collecting a RemainAfterExit unit.
	if !state.Active && !state.Populated {
		final.Code, final.Signaled = state.Code, state.Signaled
	}
	_, _ = system(ctx, "/usr/bin/systemctl", "stop", r.Unit)
	_, _ = system(ctx, "/usr/bin/systemctl", "reset-failed", r.Unit)
	rec.Final = &final
	if err = s.save(rec); err != nil {
		return Response{}, err
	}
	return Response{State: &final}, nil
}
func (s *Server) Serve(ctx context.Context) error {
	lock, err := os.OpenFile(filepath.Join(s.policy.StateDir, "daemon.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return errors.New("another native broker owns this state directory")
	}
	if info, statErr := os.Lstat(s.policy.Socket); statErr == nil {
		st := info.Sys().(*syscall.Stat_t)
		if info.Mode()&os.ModeSocket == 0 || int(st.Uid) != s.policy.ControlUID || info.Mode().Perm() != 0600 {
			return errors.New("refusing to replace an unexpected broker socket")
		}
		c, dialErr := net.DialUnix("unixpacket", nil, &net.UnixAddr{Name: s.policy.Socket, Net: "unixpacket"})
		if dialErr == nil {
			c.Close()
			return errors.New("native broker socket is already active")
		}
		if err = os.Remove(s.policy.Socket); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: s.policy.Socket, Net: "unixpacket"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chown(s.policy.Socket, s.policy.ControlUID, s.policy.ControlGID); err != nil {
		return err
	}
	if err = os.Chmod(s.policy.Socket, 0600); err != nil {
		return err
	}
	go func() { <-ctx.Done(); _ = listener.Close() }()
	limit := make(chan struct{}, 32)
	for {
		c, err := listener.AcceptUnix()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case limit <- struct{}{}:
		case <-ctx.Done():
			c.Close()
			return nil
		}
		go func() {
			defer func() { <-limit }()
			defer c.Close()
			_ = c.SetDeadline(time.Now().Add(30 * time.Second))
			uid, _, err := peerUID(c)
			if err != nil || uid != s.policy.ControlUID {
				_ = sendPacket(c, []byte(`{"error":"caller is not the control plane"}`), nil)
				return
			}
			raw, files, err := receivePacket(c)
			for _, f := range files {
				defer f.Close()
			}
			var response Response
			if err == nil {
				var req Request
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.DisallowUnknownFields()
				err = dec.Decode(&req)
				if err == nil {
					var extra any
					if dec.Decode(&extra) != io.EOF {
						err = errors.New("trailing request data")
					}
				}
				if err == nil {
					callCtx, cancel := context.WithTimeout(ctx, 25*time.Second)
					response, err = s.dispatch(callCtx, req, files)
					cancel()
				}
			}
			if err != nil {
				response = Response{Error: err.Error()}
			}
			out, err := json.Marshal(response)
			if err == nil {
				_ = sendPacket(c, out, nil)
			}
		}()
	}
}
