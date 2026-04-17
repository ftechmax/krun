package runtime

import "testing"

func TestReleaseManifestURL(t *testing.T) {
	t.Parallel()

	testCases := []struct {
		name      string
		version   string
		wantURL   string
		wantError bool
	}{
		{
			name:    "valid core semver",
			version: "0.3.0",
			wantURL: "https://github.com/ftechmax/krun/releases/download/0.3.0/krun-traffic-manager.yaml",
		},
		{
			name:    "valid prerelease version",
			version: "1.2.3-rc.1",
			wantURL: "https://github.com/ftechmax/krun/releases/download/1.2.3-rc.1/krun-traffic-manager.yaml",
		},
		{
			name:    "valid semver with build metadata",
			version: "1.2.3-rc.1+build.11",
			wantURL: "https://github.com/ftechmax/krun/releases/download/1.2.3-rc.1+build.11/krun-traffic-manager.yaml",
		},
		{
			name:      "empty version",
			version:   "",
			wantError: true,
		},
		{
			name:      "leading whitespace",
			version:   " 0.3.0",
			wantError: true,
		},
		{
			name:      "v-prefix is not allowed",
			version:   "v0.3.0",
			wantError: true,
		},
		{
			name:      "missing patch component",
			version:   "1.2",
			wantError: true,
		},
		{
			name:      "leading zeros in numeric identifier",
			version:   "01.2.3",
			wantError: true,
		},
		{
			name:      "contains slash",
			version:   "1/2",
			wantError: true,
		},
		{
			name:      "contains path traversal",
			version:   "../1.2.3",
			wantError: true,
		},
		{
			name:      "contains query characters",
			version:   "1.2.3?x=1",
			wantError: true,
		},
		{
			name:      "contains encoded slash marker",
			version:   "1.2.3%2Fmalicious",
			wantError: true,
		},
	}

	for _, testCase := range testCases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			gotURL, err := releaseManifestURL(testCase.version)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("expected error for version %q, got URL %q", testCase.version, gotURL)
				}
				return
			}

			if err != nil {
				t.Fatalf("unexpected error for version %q: %v", testCase.version, err)
			}
			if gotURL != testCase.wantURL {
				t.Fatalf("unexpected URL: want %q, got %q", testCase.wantURL, gotURL)
			}
		})
	}
}
