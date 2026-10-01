//go:build linux

package nativebroker

import (
	"testing"
)

func provisionFixture() (Policy, provisionAccounts) {
	p := fixturePolicy()
	p.Apps[0].BundleRoot = "/srv/apps/alpha/versions"
	a := provisionAccounts{users: map[int]provisionAccount{2000: {name: "controller", uid: 2000, gid: 2000}}, groups: map[int]provisionGroup{2000: {name: "controller", gid: 2000}}}
	return p, a
}
func TestProvisionRejectsIdentityReuseAndSharedAuthority(t *testing.T) {
	cases := map[string]func(*Policy, *provisionAccounts){
		"unrelated UID": func(p *Policy, a *provisionAccounts) {
			a.users[2001] = provisionAccount{"service", 2001, 2001, "/usr/sbin/nologin"}
		},
		"reassigned app": func(p *Policy, a *provisionAccounts) {
			a.users[2001] = provisionAccount{"shapp-2000-1", 2001, 2001, "/usr/sbin/nologin"}
			p.Apps[0].ID = 2
		},
		"unrelated GID": func(p *Policy, a *provisionAccounts) { a.groups[2001] = provisionGroup{"service", 2001, nil} },
		"login enabled": func(p *Policy, a *provisionAccounts) {
			a.users[2001] = provisionAccount{"shapp-2000-1", 2001, 2001, "/bin/bash"}
		},
		"supplementary access": func(p *Policy, a *provisionAccounts) {
			a.users[2001] = provisionAccount{"shapp-2000-1", 2001, 2001, "/usr/sbin/nologin"}
			a.groups[5000] = provisionGroup{"extra", 5000, []string{"shapp-2000-1"}}
		},
		"shared primary group": func(p *Policy, a *provisionAccounts) {
			a.groups[2001] = provisionGroup{"shapp-2000-1", 2001, nil}
			a.users[9000] = provisionAccount{"other", 9000, 2001, "/bin/false"}
		},
		"dangling primary group": func(p *Policy, a *provisionAccounts) {
			a.users[9000] = provisionAccount{"other", 9000, 2001, "/bin/false"}
		},
		"dangling supplementary membership": func(p *Policy, a *provisionAccounts) {
			a.groups[5000] = provisionGroup{"extra", 5000, []string{"shapp-2000-1"}}
		},
		"shared supplementary group": func(p *Policy, a *provisionAccounts) {
			a.groups[2001] = provisionGroup{"shapp-2000-1", 2001, []string{"other"}}
		},
		"unsafe slug":      func(p *Policy, a *provisionAccounts) { p.Apps[0].Slug = ".." },
		"misplaced bundle": func(p *Policy, a *provisionAccounts) { p.Apps[0].BundleRoot = "/srv/apps/alpha" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p, a := provisionFixture()
			mutate(&p, &a)
			if _, err := provisionIdentityPlan(p, a); err == nil {
				t.Fatal("unsafe provisioning accepted")
			}
		})
	}
}
func TestProvisionIdentityPlanResumesWithoutRecreatingAccounts(t *testing.T) {
	p, a := provisionFixture()
	plan, err := provisionIdentityPlan(p, a)
	if err != nil || len(plan) != 3 {
		t.Fatalf("new identity: %v %v", plan, err)
	}
	a.groups[2001] = provisionGroup{"shapp-2000-1", 2001, nil}
	plan, err = provisionIdentityPlan(p, a)
	if err != nil || len(plan) != 2 {
		t.Fatalf("partial group creation: %v %v", plan, err)
	}
	a.users[2001] = provisionAccount{"shapp-2000-1", 2001, 2001, "/usr/sbin/nologin"}
	a.groups[2001] = provisionGroup{"shapp-2000-1", 2001, []string{"controller"}}
	plan, err = provisionIdentityPlan(p, a)
	if err != nil || len(plan) != 0 {
		t.Fatalf("idempotent identity: %v %v", plan, err)
	}
}
func TestProvisionRejectsOverlappingAppParents(t *testing.T) {
	p, _ := provisionFixture()
	p.Apps[0].DataRoot = "/srv/apps/alpha/data/alpha"
	if _, err := provisionDirectoryPlan(p); err == nil {
		t.Fatal("overlapping app parent accepted")
	}
}
