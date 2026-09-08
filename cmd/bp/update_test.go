package main

import (
	"blueprint/internal/release"
	"os"
	"path/filepath"
	"testing"
)

func TestNpmManagedPathRequiresPackageAndNativeLayout(t *testing.T) {
	for _, tc := range []struct {
		name, pkg, file string
		want            bool
	}{
		{"npm native", `{"name":"@tunapro/blueprint"}`, release.Platform(), true},
		{"other package", `{"name":"other"}`, release.Platform(), false},
		{"invalid metadata", `{`, release.Platform(), false},
		{"standalone binary", `{"name":"@tunapro/blueprint"}`, "bp", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Mkdir(filepath.Join(root, "bin"), 0700); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(root, "bin", tc.file)
			if err := os.WriteFile(target, []byte("fixture"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(tc.pkg), 0600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "linked-bp")
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if got := npmManagedPath(link); got != tc.want {
				t.Fatalf("npm managed=%v, want %v", got, tc.want)
			}
		})
	}
}
