package fs

import "testing"

func TestWindowsReadRootName(t *testing.T) {
	tests := []struct{ name, root, want string }{
		{name: "drive", root: `C:\workspace`, want: `\??\C:\workspace`},
		{name: "UNC", root: `\\server\share\workspace`, want: `\??\UNC\server\share\workspace`},
		{name: "extended drive", root: `\\?\C:\workspace`, want: `\??\C:\workspace`},
		{name: "extended UNC", root: `\\?\UNC\server\share\workspace`, want: `\??\UNC\server\share\workspace`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := windowsReadRootName(test.root); got != test.want {
				t.Fatalf("NT root = %q, want %q", got, test.want)
			}
		})
	}
}
