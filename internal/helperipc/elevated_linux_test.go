//go:build linux

package helperipc

import "testing"

func TestInvokingUser(t *testing.T) {
	for _, tc := range []struct {
		name       string
		uid, gid   string
		wantUID    int
		wantGID    int
		wantCaller bool
	}{
		{"sudo caller", " 1000 ", " 1001 ", 1000, 1001, true},
		{"root caller", "0", "0", 0, 0, true},
		{"missing group", "1000", "", 1000, -1, true},
		{"invalid group", "1000", "invalid", 1000, -1, true},
		{"negative group", "1000", "-2", 1000, -1, true},
		{"missing sudo caller", "", "", 0, 0, false},
		{"invalid caller", "invalid", "1000", 0, 0, false},
		{"negative caller", "-1", "1000", 0, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := map[string]string{"SUDO_UID": tc.uid, "SUDO_GID": tc.gid, "PKEXEC_UID": "2000"}
			uid, gid, ok := invokingUser(func(key string) string { return env[key] })
			if uid != tc.wantUID || gid != tc.wantGID || ok != tc.wantCaller {
				t.Fatalf("invoking user = (%d, %d, %t), want (%d, %d, %t)", uid, gid, ok, tc.wantUID, tc.wantGID, tc.wantCaller)
			}
		})
	}
}
